// MongoDB drivers load-test HTTP service — Node.js implementation.
//
// Implements the four /v1 endpoints in spec/http-api.md against the `mongodb`
// Node driver. Single MongoClient for the process lifetime; per-request the
// dispatcher runs each op sequentially and never short-circuits.

import Fastify from 'fastify';
import {
  MongoClient,
  ObjectId,
  MongoServerError,
  MongoNetworkError,
  MongoNetworkTimeoutError,
  MongoServerSelectionError,
  MongoBulkWriteError,
} from 'mongodb';
import { EJSON } from 'bson';
import { readFileSync } from 'node:fs';

// ---------- info ----------

const SPEC_VERSION = '1.0.0';

function readDriverVersion() {
  try {
    const pkg = JSON.parse(
      readFileSync(new URL('./node_modules/mongodb/package.json', import.meta.url), 'utf8'),
    );
    return pkg.version;
  } catch {
    return 'unknown';
  }
}

const INFO = Object.freeze({
  driver: 'mongodb (node)',
  driver_version: readDriverVersion(),
  language: 'node',
  language_version: process.versions.node,
  spec_version: SPEC_VERSION,
});

// ---------- error classification (spec §7) ----------

const CODE_DUPLICATE = 'DUPLICATE_KEY';
const CODE_WRITECONFLICT = 'WRITE_CONFLICT';
const CODE_TIMEOUT = 'TIMEOUT';
const CODE_NETWORK = 'NETWORK';
const CODE_AUTH = 'AUTH';
const CODE_NOT_FOUND = 'NOT_FOUND';
const CODE_INVALID_ARG = 'INVALID_ARGUMENT';
const CODE_INTERNAL = 'INTERNAL';

function codeFromServer(code) {
  switch (code) {
    case 11000:
    case 11001:
    case 12582:
      return CODE_DUPLICATE;
    case 112:
      return CODE_WRITECONFLICT;
    case 50:
    case 89:
    case 262:
      return CODE_TIMEOUT;
    case 13:
    case 18:
    case 8000:
      return CODE_AUTH;
    case 26:
    case 27:
      return CODE_NOT_FOUND;
    case 2:
    case 4:
    case 9:
    case 14:
    case 16:
    case 17:
    case 30:
    case 31:
    case 40:
    case 51:
    case 52:
    case 66:
    case 72:
    case 73:
      return CODE_INVALID_ARG;
    default:
      return CODE_INTERNAL;
  }
}

// classifyErr returns {code, message, server_code?}.
function classifyErr(err) {
  const message = err?.message ?? String(err);

  // BulkWriteError carries writeErrors[].code and may also have err.code.
  if (err instanceof MongoBulkWriteError) {
    const first = err.writeErrors?.[0];
    const sc =
      typeof first?.code === 'number'
        ? first.code
        : typeof first?.err?.code === 'number'
          ? first.err.code
          : typeof err.code === 'number'
            ? err.code
            : undefined;
    if (typeof sc === 'number') {
      return { code: codeFromServer(sc), message, server_code: sc };
    }
    return { code: CODE_INTERNAL, message };
  }

  // Network classifications first — these can also carry a .code that is
  // unrelated to the server.
  if (err instanceof MongoNetworkTimeoutError) {
    return { code: CODE_TIMEOUT, message };
  }
  if (err instanceof MongoServerSelectionError) {
    return { code: CODE_NETWORK, message };
  }
  if (err instanceof MongoNetworkError) {
    return { code: CODE_NETWORK, message };
  }

  // Server-coded error.
  if (err instanceof MongoServerError) {
    const sc = typeof err.code === 'number' ? err.code : undefined;
    if (typeof sc === 'number') {
      return { code: codeFromServer(sc), message, server_code: sc };
    }
    return { code: CODE_INTERNAL, message };
  }

  // Some driver errors expose .code without being a MongoServerError (e.g.
  // duplicate-key on insertOne can surface as a generic MongoError with code).
  if (err && typeof err.code === 'number') {
    return { code: codeFromServer(err.code), message, server_code: err.code };
  }
  // Some driver errors expose ErrorLabels including "TransientTransactionError"
  // or names that hint at type — fall back to INTERNAL otherwise.
  const name = err?.constructor?.name ?? err?.name ?? '';
  if (/Timeout/i.test(name)) return { code: CODE_TIMEOUT, message };
  if (/Network|TopologyDescription/i.test(name)) return { code: CODE_NETWORK, message };
  if (/Auth/i.test(name)) return { code: CODE_AUTH, message };
  return { code: CODE_INTERNAL, message };
}

// ---------- EJSON helpers ----------

// parseEJSON converts a JS value (already parsed from the JSON request body)
// into BSON-typed values (ObjectId, Decimal128, Date, etc.). Relaxed mode is
// fine for input because numeric envelopes (`{"$numberInt": ...}`) are still
// recognized.
function parseEJSON(v) {
  return EJSON.deserialize(v, { relaxed: true });
}

// stringifyEJSON serializes a BSON value to the relaxed Extended JSON shape
// required by spec §5.1.
function stringifyEJSON(v) {
  return EJSON.stringify(v, { relaxed: true });
}

// ejsonValue converts a single BSON value (ObjectId, Long, number, etc.) into
// the JS form that JSON.stringify would emit in relaxed Extended JSON.
function ejsonValue(v) {
  return JSON.parse(stringifyEJSON(v));
}

// ---------- request validation ----------

class RequestError extends Error {
  constructor(code, message) {
    super(message);
    this.errCode = code;
  }
}

const SCHEMA_VIOLATION = 'SCHEMA_VIOLATION';
const UNKNOWN_OP = 'UNKNOWN_OP';
const EMPTY_OPS = 'EMPTY_OPS';
const MISSING_FIELD = 'MISSING_FIELD';
const BAD_REQUEST = 'BAD_REQUEST';

const OP_NAMES = new Set([
  'insertOne',
  'insertMany',
  'find',
  'updateOne',
  'updateMany',
  'replaceOne',
  'deleteOne',
  'deleteMany',
  'countDocuments',
  'aggregate',
  'bulkWrite',
]);

function isPlainObject(v) {
  return v !== null && typeof v === 'object' && !Array.isArray(v);
}

function validateOp(op, inBulk, path) {
  if (!isPlainObject(op)) {
    throw new RequestError(SCHEMA_VIOLATION, `${path}: op must be an object`);
  }
  if (typeof op.name !== 'string' || op.name === '') {
    throw new RequestError(MISSING_FIELD, `${path}: missing required field "name"`);
  }
  if (!inBulk) {
    if (typeof op.collection !== 'string' || op.collection === '') {
      throw new RequestError(SCHEMA_VIOLATION, `${path}: collection must be non-empty`);
    }
  }
  if (inBulk && op.name === 'bulkWrite') {
    throw new RequestError(UNKNOWN_OP, `${path}: nested bulkWrite is not allowed`);
  }
  if (!OP_NAMES.has(op.name)) {
    throw new RequestError(UNKNOWN_OP, `${path}: unknown op name "${op.name}"`);
  }
  const need = (f) => {
    if (op[f] === undefined || op[f] === null) {
      throw new RequestError(MISSING_FIELD, `${path}: missing required field "${f}"`);
    }
  };
  switch (op.name) {
    case 'insertOne':
      need('document');
      break;
    case 'insertMany':
      need('documents');
      if (!Array.isArray(op.documents) || op.documents.length === 0) {
        throw new RequestError(SCHEMA_VIOLATION, `${path}: documents must be a non-empty array`);
      }
      break;
    case 'find':
      need('filter');
      break;
    case 'updateOne':
    case 'updateMany':
      need('filter');
      need('update');
      break;
    case 'replaceOne':
      need('filter');
      need('replacement');
      break;
    case 'deleteOne':
    case 'deleteMany':
      need('filter');
      break;
    case 'countDocuments':
      need('filter');
      break;
    case 'aggregate':
      need('pipeline');
      if (!Array.isArray(op.pipeline)) {
        throw new RequestError(SCHEMA_VIOLATION, `${path}: pipeline must be an array`);
      }
      break;
    case 'bulkWrite':
      need('operations');
      if (!Array.isArray(op.operations) || op.operations.length === 0) {
        throw new RequestError(SCHEMA_VIOLATION, `${path}: operations must be a non-empty array`);
      }
      op.operations.forEach((sub, i) =>
        validateOp(sub, true, `${path}.operations[${i}]`),
      );
      break;
  }
}

function validateOpsRequest(body) {
  if (!isPlainObject(body)) {
    throw new RequestError(SCHEMA_VIOLATION, 'request body must be a JSON object');
  }
  if (body.database === undefined || body.database === null) {
    throw new RequestError(MISSING_FIELD, 'missing required field "database"');
  }
  if (typeof body.database !== 'string' || body.database === '') {
    throw new RequestError(SCHEMA_VIOLATION, '`database` must be a non-empty string');
  }
  if (body.ops === undefined || body.ops === null) {
    throw new RequestError(MISSING_FIELD, 'missing required field "ops"');
  }
  if (!Array.isArray(body.ops)) {
    throw new RequestError(SCHEMA_VIOLATION, '`ops` must be an array');
  }
  if (body.ops.length === 0) {
    throw new RequestError(EMPTY_OPS, 'ops must be a non-empty array');
  }
  body.ops.forEach((op, i) => validateOp(op, false, `ops[${i}]`));
}

// ---------- op dispatch ----------

async function runOp(db, op) {
  switch (op.name) {
    case 'insertOne':
      return await opInsertOne(db, op);
    case 'insertMany':
      return await opInsertMany(db, op);
    case 'find':
      return await opFind(db, op);
    case 'updateOne':
      return await opUpdateOne(db, op);
    case 'updateMany':
      return await opUpdateMany(db, op);
    case 'replaceOne':
      return await opReplaceOne(db, op);
    case 'deleteOne':
      return await opDeleteOne(db, op);
    case 'deleteMany':
      return await opDeleteMany(db, op);
    case 'countDocuments':
      return await opCountDocuments(db, op);
    case 'aggregate':
      return await opAggregate(db, op);
    case 'bulkWrite':
      return await opBulkWrite(db, op);
    default:
      // Should never happen — caught by validateOp earlier.
      throw new Error(`unknown op ${op.name}`);
  }
}

async function opInsertOne(db, op) {
  const doc = parseEJSON(op.document);
  const res = await db.collection(op.collection).insertOne(doc);
  return { inserted_id: ejsonValue(res.insertedId) };
}

async function opInsertMany(db, op) {
  const docs = op.documents.map(parseEJSON);
  const opts = {};
  if (typeof op.ordered === 'boolean') opts.ordered = op.ordered;
  const res = await db.collection(op.collection).insertMany(docs, opts);
  // insertedIds is keyed by integer position; iterate by index to preserve order.
  const ids = [];
  for (let i = 0; i < docs.length; i++) {
    ids.push(ejsonValue(res.insertedIds[i]));
  }
  return { inserted_ids: ids, inserted_count: res.insertedCount };
}

async function opFind(db, op) {
  const filter = parseEJSON(op.filter);
  const opts = {};
  if (op.projection !== undefined) opts.projection = parseEJSON(op.projection);
  if (op.sort !== undefined) opts.sort = parseEJSON(op.sort);
  if (typeof op.skip === 'number') opts.skip = op.skip;
  if (typeof op.limit === 'number') opts.limit = op.limit;
  const cursor = db.collection(op.collection).find(filter, opts);
  const docs = await cursor.toArray();
  return {
    documents: docs.map(ejsonValue),
    count: docs.length,
  };
}

// updateOrPipeline decodes update spec, which may be a doc or a pipeline.
function updateOrPipeline(v) {
  if (Array.isArray(v)) return v.map(parseEJSON);
  return parseEJSON(v);
}

function arrayFilters(v) {
  if (!Array.isArray(v)) return undefined;
  return v.map(parseEJSON);
}

function updateDataToJSON(res) {
  const out = {
    matched_count: res.matchedCount,
    modified_count: res.modifiedCount,
  };
  if (res.upsertedCount && res.upsertedCount > 0 && res.upsertedId != null) {
    out.upserted_id = ejsonValue(res.upsertedId);
  }
  return out;
}

async function opUpdateOne(db, op) {
  const filter = parseEJSON(op.filter);
  const update = updateOrPipeline(op.update);
  const opts = {};
  if (typeof op.upsert === 'boolean') opts.upsert = op.upsert;
  const af = arrayFilters(op.array_filters);
  if (af) opts.arrayFilters = af;
  const res = await db.collection(op.collection).updateOne(filter, update, opts);
  return updateDataToJSON(res);
}

async function opUpdateMany(db, op) {
  const filter = parseEJSON(op.filter);
  const update = updateOrPipeline(op.update);
  const opts = {};
  if (typeof op.upsert === 'boolean') opts.upsert = op.upsert;
  const af = arrayFilters(op.array_filters);
  if (af) opts.arrayFilters = af;
  const res = await db.collection(op.collection).updateMany(filter, update, opts);
  return updateDataToJSON(res);
}

async function opReplaceOne(db, op) {
  const filter = parseEJSON(op.filter);
  const replacement = parseEJSON(op.replacement);
  const opts = {};
  if (typeof op.upsert === 'boolean') opts.upsert = op.upsert;
  const res = await db.collection(op.collection).replaceOne(filter, replacement, opts);
  return updateDataToJSON(res);
}

async function opDeleteOne(db, op) {
  const filter = parseEJSON(op.filter);
  const res = await db.collection(op.collection).deleteOne(filter);
  return { deleted_count: res.deletedCount };
}

async function opDeleteMany(db, op) {
  const filter = parseEJSON(op.filter);
  const res = await db.collection(op.collection).deleteMany(filter);
  return { deleted_count: res.deletedCount };
}

async function opCountDocuments(db, op) {
  const filter = parseEJSON(op.filter);
  const count = await db.collection(op.collection).countDocuments(filter);
  return { count };
}

async function opAggregate(db, op) {
  const pipeline = op.pipeline.map(parseEJSON);
  const cursor = db.collection(op.collection).aggregate(pipeline);
  const docs = await cursor.toArray();
  return {
    documents: docs.map(ejsonValue),
    count: docs.length,
  };
}

// Mongo Node driver's bulk-write insertOne does NOT auto-generate _id and
// expose it via BulkWriteResult.insertedIds across all driver versions for
// nested bulk inserts. The spec lets us pre-assign one, which makes the
// behavior predictable: client-side ObjectId for every insertOne whose
// document lacks _id.
async function opBulkWrite(db, op) {
  const ops = [];
  const insertedIdsByIndex = {}; // sub-op index -> pre-assigned id
  op.operations.forEach((sub, i) => {
    switch (sub.name) {
      case 'insertOne': {
        const doc = parseEJSON(sub.document);
        if (doc._id === undefined) {
          doc._id = new ObjectId();
        }
        insertedIdsByIndex[i] = doc._id;
        ops.push({ insertOne: { document: doc } });
        break;
      }
      case 'updateOne': {
        const o = {
          filter: parseEJSON(sub.filter),
          update: updateOrPipeline(sub.update),
        };
        if (typeof sub.upsert === 'boolean') o.upsert = sub.upsert;
        const af = arrayFilters(sub.array_filters);
        if (af) o.arrayFilters = af;
        ops.push({ updateOne: o });
        break;
      }
      case 'updateMany': {
        const o = {
          filter: parseEJSON(sub.filter),
          update: updateOrPipeline(sub.update),
        };
        if (typeof sub.upsert === 'boolean') o.upsert = sub.upsert;
        const af = arrayFilters(sub.array_filters);
        if (af) o.arrayFilters = af;
        ops.push({ updateMany: o });
        break;
      }
      case 'replaceOne': {
        const o = {
          filter: parseEJSON(sub.filter),
          replacement: parseEJSON(sub.replacement),
        };
        if (typeof sub.upsert === 'boolean') o.upsert = sub.upsert;
        ops.push({ replaceOne: o });
        break;
      }
      case 'deleteOne':
        ops.push({ deleteOne: { filter: parseEJSON(sub.filter) } });
        break;
      case 'deleteMany':
        ops.push({ deleteMany: { filter: parseEJSON(sub.filter) } });
        break;
      default:
        throw new Error(`bulkWrite sub-op ${sub.name} unsupported`);
    }
  });

  const opts = {};
  if (typeof op.ordered === 'boolean') opts.ordered = op.ordered;
  const res = await db.collection(op.collection).bulkWrite(ops, opts);

  const insertedIds = {};
  for (const [idx, id] of Object.entries(insertedIdsByIndex)) {
    insertedIds[String(idx)] = ejsonValue(id);
  }
  const upsertedIds = {};
  // res.upsertedIds is a plain object keyed by integer index in node mongodb.
  if (res.upsertedIds && typeof res.upsertedIds === 'object') {
    for (const [idx, id] of Object.entries(res.upsertedIds)) {
      upsertedIds[String(idx)] = ejsonValue(id);
    }
  }

  return {
    inserted_count: res.insertedCount ?? 0,
    matched_count: res.matchedCount ?? 0,
    modified_count: res.modifiedCount ?? 0,
    deleted_count: res.deletedCount ?? 0,
    upserted_count: res.upsertedCount ?? 0,
    inserted_ids: insertedIds,
    upserted_ids: upsertedIds,
  };
}

async function dispatch(client, body) {
  const db = client.db(body.database);
  const results = [];
  for (const op of body.ops) {
    try {
      const data = await runOp(db, op);
      results.push({ op: op.name, ok: true, data });
    } catch (err) {
      const cl = classifyErr(err);
      const errObj = { code: cl.code, message: cl.message };
      if (typeof cl.server_code === 'number') errObj.server_code = cl.server_code;
      results.push({ op: op.name, ok: false, error: errObj });
    }
  }
  return { results };
}

// ---------- HTTP server ----------

async function main() {
  const uri = process.env.MONGODB_URI;
  if (!uri) {
    console.error('MONGODB_URI is required');
    process.exit(1);
  }
  const port = parseInt(process.env.PORT ?? '8080', 10);

  const client = new MongoClient(uri);
  await client.connect();
  try {
    await client.db('admin').command({ ping: 1 });
  } catch (e) {
    console.warn(`startup ping failed (continuing): ${e?.message ?? e}`);
  }

  const app = Fastify({
    logger: false,
    bodyLimit: 16 * 1024 * 1024,
  });

  // Force a fixed Content-Type on every response.
  app.addHook('onSend', async (req, reply, payload) => {
    reply.header('content-type', 'application/json; charset=utf-8');
    return payload;
  });

  // Custom serializer so we don't accidentally lose BSON-shape strings if the
  // dispatcher returns nested values; everything we put in `data` is already
  // plain JS, so JSON.stringify works.
  // (Fastify's default JSON serializer is fine here.)

  app.get('/v1/info', async () => INFO);

  app.get('/v1/health', async (req, reply) => {
    try {
      await client.db('admin').command({ ping: 1 });
      return { ok: true };
    } catch (err) {
      reply.code(503);
      return { ok: false, detail: err?.message ?? String(err) };
    }
  });

  app.post('/v1/admin/reset', async (req, reply) => {
    const body = req.body;
    if (!isPlainObject(body) || !Array.isArray(body.databases) || body.databases.length === 0) {
      reply.code(400);
      return reqErrorBody(EMPTY_OPS, 'databases must be a non-empty array');
    }
    for (const name of body.databases) {
      if (typeof name !== 'string' || name === '') {
        reply.code(400);
        return reqErrorBody(SCHEMA_VIOLATION, 'database name must be a non-empty string');
      }
      if (name === 'admin' || name === 'local' || name === 'config') {
        reply.code(400);
        return reqErrorBody(BAD_REQUEST, `refusing to drop ${JSON.stringify(name)}`);
      }
    }
    const dropped = [];
    for (const name of body.databases) {
      try {
        await client.db(name).dropDatabase();
      } catch (err) {
        reply.code(500);
        return reqErrorBody(BAD_REQUEST, `drop ${name}: ${err?.message ?? err}`);
      }
      dropped.push(name);
    }
    return { dropped };
  });

  app.post('/v1/ops', async (req, reply) => {
    try {
      validateOpsRequest(req.body);
    } catch (err) {
      if (err instanceof RequestError) {
        reply.code(400);
        return reqErrorBody(err.errCode, err.message);
      }
      reply.code(400);
      return reqErrorBody(SCHEMA_VIOLATION, err?.message ?? String(err));
    }
    return await dispatch(client, req.body);
  });

  // Custom 400 for JSON parse failure / unknown route.
  app.setErrorHandler((err, req, reply) => {
    if (err.statusCode === 400 || err.name === 'SyntaxError') {
      reply.code(400);
      return reqErrorBody(SCHEMA_VIOLATION, err.message ?? 'bad request');
    }
    if (err.statusCode === 413) {
      reply.code(413);
      return reqErrorBody(BAD_REQUEST, 'payload too large');
    }
    reply.code(err.statusCode ?? 500);
    return reqErrorBody(BAD_REQUEST, err.message ?? 'internal error');
  });

  const shutdown = async (sig) => {
    console.log(`shutting down (${sig})`);
    try {
      await app.close();
    } catch {}
    try {
      await client.close();
    } catch {}
    process.exit(0);
  };
  process.on('SIGTERM', () => shutdown('SIGTERM'));
  process.on('SIGINT', () => shutdown('SIGINT'));

  await app.listen({ host: '0.0.0.0', port });
  console.log(`listening on :${port}`);
}

function reqErrorBody(code, message) {
  return { error: { code, message } };
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
