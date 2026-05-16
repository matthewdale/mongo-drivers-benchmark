package com.mongodb.loadtest;

import com.fasterxml.jackson.databind.JsonNode;
import org.bson.BsonArray;
import org.bson.BsonDocument;
import org.bson.BsonValue;
import org.bson.codecs.BsonValueCodec;
import org.bson.codecs.DecoderContext;
import org.bson.codecs.EncoderContext;
import org.bson.conversions.Bson;
import org.bson.json.JsonMode;
import org.bson.json.JsonReader;
import org.bson.json.JsonWriter;
import org.bson.json.JsonWriterSettings;

import java.io.StringWriter;
import java.util.ArrayList;
import java.util.List;

/**
 * Extended JSON v2 (relaxed) bridge. Per spec §5.1 (as amended), numeric BSON
 * types are emitted as plain JSON numbers and non-numeric BSON types use the
 * canonical {$...} envelope. The driver's JsonMode.RELAXED produces exactly
 * that shape.
 */
final class Ejson {
    private static final JsonWriterSettings RELAXED =
            JsonWriterSettings.builder().outputMode(JsonMode.RELAXED).build();
    private static final BsonValueCodec VALUE_CODEC = new BsonValueCodec();

    private Ejson() {}

    /** Parse a JSON value (Extended JSON or plain JSON) into a BsonDocument. */
    static BsonDocument parseDoc(JsonNode node) {
        if (node == null || node.isNull()) return new BsonDocument();
        String json = node.toString();
        return BsonDocument.parse(json);
    }

    /** Parse an array of JSON values into a list of BsonDocuments. */
    static List<BsonDocument> parseDocs(JsonNode arrayNode) {
        List<BsonDocument> out = new ArrayList<>();
        if (arrayNode == null || arrayNode.isNull()) return out;
        for (int i = 0; i < arrayNode.size(); i++) {
            out.add(parseDoc(arrayNode.get(i)));
        }
        return out;
    }

    /**
     * Parse an update value, which may be a document (operator-style update)
     * or an array of pipeline stages (aggregation-pipeline update).
     */
    static Object parseUpdate(JsonNode node) {
        if (node == null || node.isNull()) return new BsonDocument();
        if (node.isArray()) return parseDocs(node);
        return parseDoc(node);
    }

    /** Serialize a BsonDocument to relaxed Extended JSON v2. */
    static String marshalDoc(BsonDocument doc) {
        return doc.toJson(RELAXED);
    }

    /**
     * Serialize an arbitrary BsonValue (scalar or compound) to relaxed
     * Extended JSON v2. We wrap scalars in a single-field document and slice
     * the value back out via the driver's JsonWriter for the array case, but
     * for the common cases (ObjectId, Int32, Int64, Double, Decimal128,
     * String, Boolean, DateTime, Binary, Document, Array) we get correct
     * output by writing the value directly with the BsonValueCodec.
     */
    static String marshalValue(BsonValue v) {
        if (v == null) return "null";
        StringWriter sw = new StringWriter();
        JsonWriter writer = new JsonWriter(sw, RELAXED);
        // BsonValueCodec writes the value with its element type but expects to
        // be inside a document/array context. Wrap in a single-element doc and
        // emit only the value via the codec for the doc case; for everything
        // else, write a wrapper { "v": <value> } and slice.
        writer.writeStartDocument();
        writer.writeName("v");
        VALUE_CODEC.encode(writer, v, EncoderContext.builder().build());
        writer.writeEndDocument();
        String wrapped = sw.toString();
        // wrapped is like {"v": <jsonValue>}; extract the value part.
        // We parse it back with Jackson to be safe.
        try {
            JsonNode root = JacksonHolder.MAPPER.readTree(wrapped);
            JsonNode val = root.get("v");
            return val == null ? "null" : val.toString();
        } catch (Exception e) {
            // Should not happen — the driver produced valid JSON.
            throw new RuntimeException("marshalValue unwrap failed: " + wrapped, e);
        }
    }

    /** Encode a Java string as a JSON string literal. */
    static String jsonString(String s) {
        StringBuilder sb = new StringBuilder(s.length() + 2);
        sb.append('"');
        for (int i = 0; i < s.length(); i++) {
            char c = s.charAt(i);
            switch (c) {
                case '"' -> sb.append("\\\"");
                case '\\' -> sb.append("\\\\");
                case '\n' -> sb.append("\\n");
                case '\r' -> sb.append("\\r");
                case '\t' -> sb.append("\\t");
                case '\b' -> sb.append("\\b");
                case '\f' -> sb.append("\\f");
                default -> {
                    if (c < 0x20) sb.append(String.format("\\u%04x", (int) c));
                    else sb.append(c);
                }
            }
        }
        sb.append('"');
        return sb.toString();
    }

    /** Lazily holds a Jackson ObjectMapper so Ejson doesn't depend on Routes. */
    private static final class JacksonHolder {
        static final com.fasterxml.jackson.databind.ObjectMapper MAPPER =
                new com.fasterxml.jackson.databind.ObjectMapper();
    }
}
