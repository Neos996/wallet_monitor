// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

// Minimal ERC20-like contract for local log-based monitoring tests.
// It only needs:
// - standard Transfer event topic
// - decimals() for metadata cache
// - a function that emits multiple Transfer logs in one tx (log_index test)
contract FakeERC20 {
    event Transfer(address indexed from, address indexed to, uint256 value);

    string public name;
    string public symbol;
    uint8 private _decimals;

    uint256 public totalSupply;
    mapping(address => uint256) public balanceOf;

    constructor(string memory name_, string memory symbol_, uint8 decimals_) {
        name = name_;
        symbol = symbol_;
        _decimals = decimals_;
    }

    function decimals() external view returns (uint8) {
        return _decimals;
    }

    function mint(address to, uint256 value) external {
        totalSupply += value;
        balanceOf[to] += value;
        emit Transfer(address(0), to, value);
    }

    function mintTwo(address to, uint256 v1, uint256 v2) external {
        totalSupply += (v1 + v2);
        balanceOf[to] += (v1 + v2);
        emit Transfer(address(0), to, v1);
        emit Transfer(address(0), to, v2);
    }
}

