// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";
import {AIArbitrator} from "../src/AIArbitrator.sol";

contract AIArbitratorTest is Test {
    AIArbitrator arb;

    address owner = makeAddr("owner");
    address oracle = makeAddr("oracle");
    address client = makeAddr("client");
    address freelancer = makeAddr("freelancer");
    address stranger = makeAddr("stranger");

    bytes32 reqHash = keccak256("requirements");
    bytes32 subHash = keccak256("submission");
    uint256 constant AMOUNT = 1 ether;
    uint64 constant REVIEW_TIMEOUT = 3 days;
    uint64 deadline;

    function setUp() public {
        vm.prank(owner);
        arb = new AIArbitrator(oracle, REVIEW_TIMEOUT);
        vm.deal(client, 10 ether);
        deadline = uint64(block.timestamp + 7 days);
    }

    // ───────────── helpers ─────────────

    function _create() internal returns (uint256 id) {
        vm.prank(client);
        id = arb.createJob{value: AMOUNT}(freelancer, reqHash, deadline);
    }

    function _createAndSubmit() internal returns (uint256 id) {
        id = _create();
        vm.prank(freelancer);
        arb.submitWork(id, subHash);
    }

    // ───────────── create ─────────────

    function test_createJob_locksFundsAndEmits() public {
        vm.expectEmit(true, true, true, true);
        emit AIArbitrator.JobFunded(1, client, freelancer, AMOUNT, reqHash, deadline);

        uint256 id = _create();

        assertEq(id, 1);
        assertEq(address(arb).balance, AMOUNT);
        AIArbitrator.Job memory j = arb.getJob(id);
        assertEq(j.client, client);
        assertEq(j.freelancer, freelancer);
        assertEq(j.amount, AMOUNT);
        assertEq(uint8(j.state), uint8(AIArbitrator.State.Funded));
    }

    function test_createJob_revertsOnBadInput() public {
        vm.startPrank(client);
        vm.expectRevert(AIArbitrator.InvalidInput.selector);
        arb.createJob{value: 0}(freelancer, reqHash, deadline); // no value

        vm.expectRevert(AIArbitrator.InvalidInput.selector);
        arb.createJob{value: AMOUNT}(address(0), reqHash, deadline); // zero freelancer

        vm.expectRevert(AIArbitrator.InvalidInput.selector);
        arb.createJob{value: AMOUNT}(client, reqHash, deadline); // self-dealing

        vm.expectRevert(AIArbitrator.InvalidInput.selector);
        arb.createJob{value: AMOUNT}(freelancer, bytes32(0), deadline); // no requirements hash

        vm.expectRevert(AIArbitrator.InvalidInput.selector);
        arb.createJob{value: AMOUNT}(freelancer, reqHash, uint64(block.timestamp)); // deadline not in future
        vm.stopPrank();
    }

    // ───────────── happy path ─────────────

    function test_happyPath_releaseToFreelancer() public {
        uint256 id = _createAndSubmit();
        uint256 before = freelancer.balance;

        vm.expectEmit(true, true, false, true);
        emit AIArbitrator.FundsReleased(id, freelancer, AMOUNT, false);
        vm.prank(oracle);
        arb.releaseFunds(id);

        assertEq(freelancer.balance, before + AMOUNT);
        assertEq(address(arb).balance, 0);
        assertEq(uint8(arb.getJob(id).state), uint8(AIArbitrator.State.Released));
    }

    // ───────────── access control ─────────────

    function test_releaseFunds_onlyOracle() public {
        uint256 id = _createAndSubmit();
        vm.prank(stranger);
        vm.expectRevert(AIArbitrator.Unauthorized.selector);
        arb.releaseFunds(id);

        vm.prank(client); // client cannot self-release either
        vm.expectRevert(AIArbitrator.Unauthorized.selector);
        arb.releaseFunds(id);
    }

    function test_submitWork_onlyFreelancer() public {
        uint256 id = _create();
        vm.prank(stranger);
        vm.expectRevert(AIArbitrator.Unauthorized.selector);
        arb.submitWork(id, subHash);
    }

    function test_releaseFunds_requiresSubmission() public {
        uint256 id = _create();
        vm.prank(oracle);
        vm.expectRevert(abi.encodeWithSelector(AIArbitrator.InvalidState.selector, AIArbitrator.State.Funded));
        arb.releaseFunds(id);
    }

    function test_doubleRelease_reverts() public {
        uint256 id = _createAndSubmit();
        vm.startPrank(oracle);
        arb.releaseFunds(id);
        vm.expectRevert(abi.encodeWithSelector(AIArbitrator.InvalidState.selector, AIArbitrator.State.Released));
        arb.releaseFunds(id);
        vm.stopPrank();
    }

    function test_setOracle_onlyOwner() public {
        vm.prank(stranger);
        vm.expectRevert(AIArbitrator.Unauthorized.selector);
        arb.setOracle(stranger);

        vm.prank(owner);
        arb.setOracle(stranger);
        assertEq(arb.oracle(), stranger);
    }

    // ───────────── deadlines & refunds ─────────────

    function test_submitWork_afterDeadline_reverts() public {
        uint256 id = _create();
        vm.warp(deadline + 1);
        vm.prank(freelancer);
        vm.expectRevert(AIArbitrator.DeadlinePassed.selector);
        arb.submitWork(id, subHash);
    }

    function test_clientRefund_beforeDeadline_reverts() public {
        uint256 id = _create();
        vm.prank(client);
        vm.expectRevert(AIArbitrator.DeadlineNotPassed.selector);
        arb.refundClient(id);
    }

    function test_clientRefund_afterDeadline_noSubmission() public {
        uint256 id = _create();
        uint256 before = client.balance;
        vm.warp(deadline + 1);
        vm.prank(client);
        arb.refundClient(id);

        assertEq(client.balance, before + AMOUNT);
        assertEq(uint8(arb.getJob(id).state), uint8(AIArbitrator.State.Refunded));
    }

    function test_clientRefund_blockedWhileSubmitted() public {
        uint256 id = _createAndSubmit();
        vm.warp(deadline + 1);
        vm.prank(client);
        vm.expectRevert(abi.encodeWithSelector(AIArbitrator.InvalidState.selector, AIArbitrator.State.Submitted));
        arb.refundClient(id);
    }

    function test_oracleRefund_anytime() public {
        uint256 id = _createAndSubmit();
        uint256 before = client.balance;
        vm.prank(oracle);
        arb.refundClient(id);
        assertEq(client.balance, before + AMOUNT);
    }

    function test_refund_strangerReverts() public {
        uint256 id = _create();
        vm.warp(deadline + 1);
        vm.prank(stranger);
        vm.expectRevert(AIArbitrator.Unauthorized.selector);
        arb.refundClient(id);
    }

    // ───────────── revisions ─────────────

    function test_reject_thenResubmit_thenRelease() public {
        uint256 id = _createAndSubmit();

        vm.prank(oracle);
        arb.rejectSubmission(id);
        assertEq(uint8(arb.getJob(id).state), uint8(AIArbitrator.State.Funded));
        assertEq(arb.getJob(id).rejections, 1);

        vm.prank(freelancer);
        arb.submitWork(id, keccak256("v2"));

        vm.prank(oracle);
        arb.releaseFunds(id);
        assertEq(uint8(arb.getJob(id).state), uint8(AIArbitrator.State.Released));
    }

    function test_reject_cappedAtMax_thenOracleRefunds() public {
        uint256 id = _createAndSubmit();
        for (uint8 i = 0; i < arb.MAX_REJECTIONS(); i++) {
            vm.prank(oracle);
            arb.rejectSubmission(id);
            vm.prank(freelancer);
            arb.submitWork(id, keccak256(abi.encode("attempt", i)));
        }
        vm.prank(oracle);
        vm.expectRevert(AIArbitrator.MaxRejectionsReached.selector);
        arb.rejectSubmission(id);

        vm.prank(oracle);
        arb.refundClient(id);
        assertEq(uint8(arb.getJob(id).state), uint8(AIArbitrator.State.Refunded));
    }

    // ───────────── oracle-silent escape hatch ─────────────

    function test_claimAfterTimeout() public {
        uint256 id = _createAndSubmit();

        vm.prank(freelancer);
        vm.expectRevert(AIArbitrator.ReviewTimeoutNotReached.selector);
        arb.claimAfterTimeout(id);

        vm.warp(block.timestamp + REVIEW_TIMEOUT + 1);
        uint256 before = freelancer.balance;
        vm.prank(freelancer);
        arb.claimAfterTimeout(id);
        assertEq(freelancer.balance, before + AMOUNT);
    }

    function test_claimAfterTimeout_onlyFreelancer() public {
        uint256 id = _createAndSubmit();
        vm.warp(block.timestamp + REVIEW_TIMEOUT + 1);
        vm.prank(stranger);
        vm.expectRevert(AIArbitrator.Unauthorized.selector);
        arb.claimAfterTimeout(id);
    }
}
