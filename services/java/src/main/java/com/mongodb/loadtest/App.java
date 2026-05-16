package com.mongodb.loadtest;

import com.mongodb.client.MongoClient;
import com.mongodb.client.MongoClients;
import io.javalin.Javalin;
import org.bson.BsonDocument;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

/**
 * Entry point for the Java load-test service. Wires the MongoDB client to the
 * four /v1 endpoints. See spec/http-api.md.
 */
public final class App {
    private static final Logger LOG = LoggerFactory.getLogger(App.class);

    private App() {}

    public static void main(String[] args) {
        String uri = System.getenv("MONGODB_URI");
        if (uri == null || uri.isEmpty()) {
            LOG.error("MONGODB_URI is required");
            System.exit(1);
        }
        int port = 8080;
        String portEnv = System.getenv("PORT");
        if (portEnv != null && !portEnv.isEmpty()) {
            try {
                port = Integer.parseInt(portEnv);
            } catch (NumberFormatException e) {
                LOG.error("Invalid PORT={}", portEnv);
                System.exit(1);
            }
        }

        MongoClient client = MongoClients.create(uri);
        try {
            client.getDatabase("admin").runCommand(new BsonDocument().append("ping", new org.bson.BsonInt32(1)));
        } catch (Exception e) {
            LOG.warn("startup ping failed (continuing): {}", e.getMessage());
        }

        Info info = new Info(
                "mongodb-driver-sync",
                driverVersion(),
                "java",
                System.getProperty("java.version"),
                "1.0.0");

        Javalin app = Routes.create(client, info);
        Runtime.getRuntime().addShutdownHook(new Thread(() -> {
            try { app.stop(); } catch (Exception ignored) {}
            try { client.close(); } catch (Exception ignored) {}
        }));
        app.start(port);
        LOG.info("listening on :{}", port);
    }

    private static String driverVersion() {
        Package p = MongoClient.class.getPackage();
        String v = p == null ? null : p.getImplementationVersion();
        if (v != null && !v.isEmpty()) return v;
        // Fallback to compile-time constant.
        return "5.7.0";
    }

    /** Static /v1/info payload. */
    public record Info(
            String driver,
            String driverVersion,
            String language,
            String languageVersion,
            String specVersion) {}
}
