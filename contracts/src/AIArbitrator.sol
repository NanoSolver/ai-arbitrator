// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title AIArbitrator
/// @notice Escrow where a single trusted oracle (our Go backend) releases funds
///         after an off-chain AI judge approves the freelancer's work.
/// @dev v1 trust model: one oracle key. Roadmap: multi-model committee + threshold signing.
contract AIArbitrator {
    // ───────────────────────── Types ─────────────────────────
    enum State {
        None, // job does not exist
        Funded, // client locked funds, waiting for (re)submission
        Submitted, // freelancer submitted, waiting for oracle verdict
        Released, // paid to freelancer (terminal)
        Refunded // returned to client (terminal)
    }

    struct Job {
        address client;
        address freelancer;
        uint256 amount;
        bytes32 requirementsHash; // keccak256 of the confirmed rubric/requirements
        bytes32 submissionHash; // keccak256 of the latest submission
        uint64 deadline; // last moment the freelancer may submit
        uint64 submittedAt;
        uint8 rejections;
        State state;
    }

    // ───────────────────────── Storage ─────────────────────────
    uint8 public constant MAX_REJECTIONS = 2;

    address public owner;
    address public oracle;
    uint64 public immutable reviewTimeout; // freelancer can self-claim if oracle is silent this long

    uint256 public nextJobId;
    mapping(uint256 => Job) private _jobs;

    // ───────────────────────── Events ─────────────────────────
    event JobFunded(
        uint256 indexed jobId,
        address indexed client,
        address indexed freelancer,
        uint256 amount,
        bytes32 requirementsHash,
        uint64 deadline
    );
    event WorkSubmitted(uint256 indexed jobId, bytes32 submissionHash, uint8 attempt);
    event SubmissionRejected(uint256 indexed jobId, uint8 rejections);
    event FundsReleased(uint256 indexed jobId, address indexed freelancer, uint256 amount, bool viaTimeout);
    event FundsRefunded(uint256 indexed jobId, address indexed client, uint256 amount);
    event OracleChanged(address indexed oldOracle, address indexed newOracle);

    // ───────────────────────── Errors ─────────────────────────
    error Unauthorized();
    error InvalidState(State current);
    error InvalidInput();
    error DeadlinePassed();
    error DeadlineNotPassed();
    error ReviewTimeoutNotReached();
    error MaxRejectionsReached();
    error TransferFailed();

    // ───────────────────────── Modifiers ─────────────────────────
    modifier onlyOracle() {
        if (msg.sender != oracle) revert Unauthorized();
        _;
    }

    modifier onlyOwner() {
        if (msg.sender != owner) revert Unauthorized();
        _;
    }

    constructor(address _oracle, uint64 _reviewTimeout) {
        if (_oracle == address(0) || _reviewTimeout == 0) revert InvalidInput();
        owner = msg.sender;
        oracle = _oracle;
        reviewTimeout = _reviewTimeout;
    }

    // ───────────────────────── Client ─────────────────────────

    /// @notice Create a job and lock msg.value in escrow.
    function createJob(address freelancer, bytes32 requirementsHash, uint64 deadline)
        external
        payable
        returns (uint256 jobId)
    {
        if (
            msg.value == 0 || freelancer == address(0) || freelancer == msg.sender || requirementsHash == bytes32(0)
                || deadline <= block.timestamp
        ) revert InvalidInput();

        jobId = ++nextJobId;
        _jobs[jobId] = Job({
            client: msg.sender,
            freelancer: freelancer,
            amount: msg.value,
            requirementsHash: requirementsHash,
            submissionHash: bytes32(0),
            deadline: deadline,
            submittedAt: 0,
            rejections: 0,
            state: State.Funded
        });

        emit JobFunded(jobId, msg.sender, freelancer, msg.value, requirementsHash, deadline);
    }

    // ───────────────────────── Freelancer ─────────────────────────

    /// @notice Submit (or resubmit after a rejection) work before the deadline.
    function submitWork(uint256 jobId, bytes32 submissionHash) external {
        Job storage job = _jobs[jobId];
        if (msg.sender != job.freelancer) revert Unauthorized();
        if (job.state != State.Funded) revert InvalidState(job.state);
        if (block.timestamp > job.deadline) revert DeadlinePassed();
        if (submissionHash == bytes32(0)) revert InvalidInput();

        job.submissionHash = submissionHash;
        job.submittedAt = uint64(block.timestamp);
        job.state = State.Submitted;

        emit WorkSubmitted(jobId, submissionHash, job.rejections);
    }

    /// @notice Escape hatch: if the oracle never responds, freelancer can claim after reviewTimeout.
    function claimAfterTimeout(uint256 jobId) external {
        Job storage job = _jobs[jobId];
        if (msg.sender != job.freelancer) revert Unauthorized();
        if (job.state != State.Submitted) revert InvalidState(job.state);
        if (block.timestamp <= job.submittedAt + reviewTimeout) revert ReviewTimeoutNotReached();

        _release(jobId, job, true);
    }

    // ───────────────────────── Oracle ─────────────────────────

    /// @notice AI verdict = PASS. Pays the freelancer.
    function releaseFunds(uint256 jobId) external onlyOracle {
        Job storage job = _jobs[jobId];
        if (job.state != State.Submitted) revert InvalidState(job.state);
        _release(jobId, job, false);
    }

    /// @notice AI verdict = FAIL with revisions left. Job returns to Funded so the freelancer can resubmit.
    function rejectSubmission(uint256 jobId) external onlyOracle {
        Job storage job = _jobs[jobId];
        if (job.state != State.Submitted) revert InvalidState(job.state);
        if (job.rejections >= MAX_REJECTIONS) revert MaxRejectionsReached(); // oracle should refund instead

        job.rejections += 1;
        job.state = State.Funded;

        emit SubmissionRejected(jobId, job.rejections);
    }

    // ───────────────────────── Refunds ─────────────────────────

    /// @notice Oracle may refund anytime while unresolved. Client may refund only if
    ///         nothing is pending review and the deadline has passed.
    function refundClient(uint256 jobId) external {
        Job storage job = _jobs[jobId];
        if (job.state != State.Funded && job.state != State.Submitted) revert InvalidState(job.state);

        if (msg.sender == oracle) {
            // allowed
        } else if (msg.sender == job.client) {
            if (job.state != State.Funded) revert InvalidState(job.state);
            if (block.timestamp <= job.deadline) revert DeadlineNotPassed();
        } else {
            revert Unauthorized();
        }

        uint256 amount = job.amount;
        job.state = State.Refunded; // effects before interaction
        _send(job.client, amount);

        emit FundsRefunded(jobId, job.client, amount);
    }

    // ───────────────────────── Admin ─────────────────────────

    function setOracle(address newOracle) external onlyOwner {
        if (newOracle == address(0)) revert InvalidInput();
        emit OracleChanged(oracle, newOracle);
        oracle = newOracle;
    }

    // ───────────────────────── Views ─────────────────────────

    function getJob(uint256 jobId) external view returns (Job memory) {
        return _jobs[jobId];
    }

    // ───────────────────────── Internal ─────────────────────────

    function _release(uint256 jobId, Job storage job, bool viaTimeout) private {
        uint256 amount = job.amount;
        job.state = State.Released; // effects before interaction
        _send(job.freelancer, amount);
        emit FundsReleased(jobId, job.freelancer, amount, viaTimeout);
    }

    function _send(address to, uint256 amount) private {
        (bool ok,) = to.call{value: amount}("");
        if (!ok) revert TransferFailed();
    }
}
