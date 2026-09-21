# TODO

## Critical

## High

- Verify Claude Code's stream-json interrupt frame, then give `broker.Adapter` an `Interrupt` method. Until it is verified the broker holds a mid-turn message and reports `held`; nothing cuts into a turn. The dispatcher can still interrupt by stopping the container, which is layer 4 and needs nothing here.

## Medium

## Low
