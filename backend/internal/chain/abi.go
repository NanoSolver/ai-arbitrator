package chain

// Minimal ABI: only what the backend calls or listens to. Must stay in sync with
// contracts/src/AIArbitrator.sol (function names, event field order and types).
const contractABI = `[
  {"type":"function","name":"releaseFunds","stateMutability":"nonpayable","inputs":[{"name":"jobId","type":"uint256"}],"outputs":[]},
  {"type":"function","name":"rejectSubmission","stateMutability":"nonpayable","inputs":[{"name":"jobId","type":"uint256"}],"outputs":[]},
  {"type":"function","name":"refundClient","stateMutability":"nonpayable","inputs":[{"name":"jobId","type":"uint256"}],"outputs":[]},
  {"type":"event","name":"JobFunded","anonymous":false,"inputs":[
    {"name":"jobId","type":"uint256","indexed":true},
    {"name":"client","type":"address","indexed":true},
    {"name":"freelancer","type":"address","indexed":true},
    {"name":"amount","type":"uint256","indexed":false},
    {"name":"requirementsHash","type":"bytes32","indexed":false},
    {"name":"deadline","type":"uint64","indexed":false}
  ]},
  {"type":"event","name":"WorkSubmitted","anonymous":false,"inputs":[
    {"name":"jobId","type":"uint256","indexed":true},
    {"name":"submissionHash","type":"bytes32","indexed":false},
    {"name":"attempt","type":"uint8","indexed":false}
  ]}
]`
