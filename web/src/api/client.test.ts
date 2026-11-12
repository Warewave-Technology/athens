import { describe, expect, it } from "vitest";
import { tpath } from "./client";

describe("tpath", () => {
  it("splits the target id into two segments", () => {
    expect(tpath("payments/orders-db")).toBe("/targets/payments/orders-db");
  });
});
