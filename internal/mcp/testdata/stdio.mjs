// Records the real SDK's stdio framing and JSON-RPC classification, for the Go port to match:
//   - lines fed to StdioServerTransport (maxBufferSize 1_048_576, as the bridge uses it) and what
//     reaches onmessage (re-serialized exactly as the bridge would write it) / onerror / onclose;
//   - the isJSONRPC* / isInitialize* predicates for a corpus of JSON values.
// Run: node stdio.mjs > fixtures/stdio.json
import { PassThrough } from 'node:stream';
import { StdioServerTransport } from '/home/user/cli/node_modules/@modelcontextprotocol/server/dist/stdio.mjs';
import {
  isInitializedNotification,
  isInitializeRequest,
  isJSONRPCErrorResponse,
  isJSONRPCNotification,
  isJSONRPCRequest,
  isJSONRPCResultResponse,
} from '/home/user/cli/node_modules/@modelcontextprotocol/client/dist/index.mjs';

const lines = [
  '{"params":{"b":1,"_meta":{"x":1,"progressToken":"t","io.modelcontextprotocol/related-task":{"taskId":"q","extra":1}},"a":2},"method":"tools/call","id":"x","jsonrpc":"2.0"}',
  '{"jsonrpc":"2.0","method":"notifications/initialized"}',
  '{"id":1,"result":{"z":1,"_meta":{"b":1,"io.modelcontextprotocol/serverInfo":{"name":"a","version":"1","x":1}},"a":2},"jsonrpc":"2.0"}',
  '{"id":2,"jsonrpc":"2.0","result":{"_meta":{"io.modelcontextprotocol/serverInfo":{"name":1}}}}',
  '{"id":1,"jsonrpc":"2.0","error":{"message":"m","code":-1,"data":{"x":1},"extra":2}}',
  '{"jsonrpc":"2.0","error":{"code":-32700,"message":"parse"}}',
  '{"id":null,"jsonrpc":"2.0","error":{"message":"m","code":1}}',
  'not json at all',
  '',
  '[{"jsonrpc":"2.0","method":"a"}]',
  '{"jsonrpc":"2.0","method":"n","params":null}',
  '{"jsonrpc":"2.0","method":"crlf"}\r',
  '{"jsonrpc":"2.0","id":1.0,"method":"one"}',
  '{"jsonrpc":"2.0","id":1e2,"method":"hundred"}',
  '{"jsonrpc":"2.0","id":9007199254740993,"method":"big"}',
  '{"jsonrpc":"2.0","id":1.5,"method":"frac"}',
  '{"jsonrpc":"2.0","id":-0,"method":"negzero"}',
  '{"jsonrpc":"2.0","method":"dup","method":"dup2","id":3}',
  '{"jsonrpc":"2.0","id":4,"method":"m","params":{"b":1,"2":2,"1":1,"__proto__":{"x":1}}}',
  '  {"jsonrpc":"2.0","id":5,"method":"ws"}  ',
  '{"jsonrpc":"2.0","id":6,"method":"m","params":{"s":"\\ud800 lone \\u00e9 \\u2028 \\u0001"}}',
  '{"jsonrpc":"2.0","id":7,"method":"m","extra":1}',
  '{"jsonrpc":"2.1","id":8,"method":"m"}',
  '{"jsonrpc":"2.0","id":9,"result":[]}',
  '{"jsonrpc":"2.0","id":10,"method":"m","params":{"n":1e400,"f":0.1,"g":1e21,"h":1e-7,"i":123456789012345680000}}',
  '{"jsonrpc":"2.0","id":11,"method":"m","params":[1,2]}',
  '{"jsonrpc":"2.0","id":12,"method":"m","params":{"_meta":{"progressToken":1.5}}}',
];

const feed = async (chunks, options) => {
  const input = new PassThrough();
  const output = new PassThrough();
  const transport = new StdioServerTransport(input, output, options);
  const events = [];
  transport.onmessage = (m) => events.push({ message: JSON.stringify(m) });
  transport.onerror = (e) => events.push({ error: e.constructor.name });
  transport.onclose = () => events.push({ close: true });
  await transport.start();
  for (const chunk of chunks) input.write(chunk);
  await new Promise((resolve) => setTimeout(resolve, 20));
  return events;
};

const invalidUtf8 = Buffer.concat([Buffer.from('{"jsonrpc":"2.0","id":13,"method":"bad'), Buffer.from([0xff, 0xc3]), Buffer.from('"}\n')]);
const framing = {
  lines: await feed([lines.map((l) => `${l}\n`).join('')], { maxBufferSize: 1_048_576 }),
  invalidUtf8: await feed([invalidUtf8], { maxBufferSize: 1_048_576 }),
  split: await feed(['{"jsonrpc":"2.0",', '"method":"split"}\n{"jsonrpc"', ':"2.0","method":"next"}\n'], { maxBufferSize: 1_048_576 }),
  oversize: await feed([`{"jsonrpc":"2.0","method":"big","params":{"s":"${'a'.repeat(1_048_576)}"}}\n`, '{"jsonrpc":"2.0","method":"after"}\n'], { maxBufferSize: 1_048_576 }),
};

const values = lines.flatMap((l) => {
  try {
    return [JSON.parse(l)];
  } catch {
    return [];
  }
});
values.push(
  { jsonrpc: '2.0', id: 1, method: 'initialize', params: { protocolVersion: '2025-06-18', capabilities: {}, clientInfo: { name: 'agent', version: '1.0.0' } } },
  { jsonrpc: '2.0', id: 1, method: 'initialize', params: { protocolVersion: '2025-06-18', capabilities: { elicitation: {} }, clientInfo: { name: 'agent' } } },
  { jsonrpc: '2.0', id: 1, method: 'initialize', params: { protocolVersion: '2025-06-18', capabilities: { elicitation: { form: { applyDefaults: 'no' } } }, clientInfo: { name: 'a', version: '1' } } },
  { jsonrpc: '2.0', id: 1, method: 'initialize', params: { protocolVersion: '2025-06-18', capabilities: { roots: { listChanged: true }, experimental: { x: { y: 1 } } }, clientInfo: { name: 'a', version: '1', icons: [{ src: 's', theme: 'dim' }] } } },
  { method: 'initialize', params: { protocolVersion: 'x', capabilities: {}, clientInfo: { name: 'a', version: '1' } }, anything: true },
  { jsonrpc: '2.0', method: 'notifications/initialized', params: { _meta: { progressToken: 'p' } } },
  { method: 'notifications/initialized', params: 5 },
);
const predicates = values.map((v) => ({
  value: JSON.stringify(v),
  request: isJSONRPCRequest(v),
  notification: isJSONRPCNotification(v),
  result: isJSONRPCResultResponse(v),
  error: isJSONRPCErrorResponse(v),
  initialize: isInitializeRequest(v),
  initialized: isInitializedNotification(v),
}));

console.log(JSON.stringify({ framing, predicates }, null, 2));
