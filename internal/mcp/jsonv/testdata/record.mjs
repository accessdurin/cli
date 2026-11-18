// Records JSON.parse + JSON.stringify round trips and Number#toString for the Go port.
// Run: node record.mjs > roundtrip.json
const inputs = [
  '{"b":1,"a":2,"10":3,"2":4,"-1":5,"01":6,"4294967295":7,"4294967294":8}',
  '{"a":1,"a":2,"b":3}',
  '[1.0,1e2,-0,0.1,1e21,1e-7,1.5e-7,123456789012345680000,5e-324,1.7976931348623157e308,1e400,-1e400,0.000001,1234.5678e10]',
  '"\\ud83d\\ude00 \\ud800 \\udc00x \\u2028 \\u0000 \\u001f \\u007f / \\/ é"',
  ' \t\n{"x" : [ ] , "y":{}}\r\n',
  '{"__proto__":{"a":1},"constructor":1}',
  '9007199254740993',
];
const numbers = [0, -0, 1, -1, 0.1, 0.2 + 0.1, 100, 1e20, 1e21, 123e-20, 1e-6, 1e-7, 2 ** 53, 2 ** 64, 1 / 3, 5e-324, Number.MAX_VALUE, 25.5, -4.25e-9];
console.log(JSON.stringify({
  roundtrip: inputs.map((input) => ({ input, output: JSON.stringify(JSON.parse(input)) })),
  invalid: ['', '{', '[1,]', '{"a":1,}', 'tru', '01', '1.', '.5', '"\\x"', '"\t"', 'NaN', '{"a" 1}', '[1 2]', '"\\ud8"'].map((input) => {
    try { JSON.parse(input); return { input, ok: true }; } catch { return { input, ok: false }; }
  }),
  numbers: numbers.map((n) => ({ bits: Array.from(new Uint8Array(new Float64Array([n]).buffer)), text: String(n) })),
}, null, 1));
