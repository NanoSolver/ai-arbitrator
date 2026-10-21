// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Script, console2} from "forge-std/Script.sol";
import {AIArbitrator} from "../src/AIArbitrator.sol";

contract Deploy is Script {
    function run() external returns (AIArbitrator arb) {
        address oracle = vm.envAddress("ORACLE_ADDRESS"); // public address of the relayer wallet
        uint256 deployerKey = vm.envUint("DEPLOYER_PRIVATE_KEY"); // 0x-prefixed hex

        vm.startBroadcast(deployerKey);
        arb = new AIArbitrator(oracle, 3 days);
        vm.stopBroadcast();

        console2.log("AIArbitrator deployed at:", address(arb));
        console2.log("Oracle:", oracle);
    }
}
