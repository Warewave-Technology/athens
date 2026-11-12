import { describe, expect, it } from "vitest";
import { toCSV } from "./csv";

describe("toCSV", () => {
  it("quotes and keeps NULL empty", () => {
    expect(
      toCSV(
        ["a", "b"],
        [
          ["x,y", null],
          ['say "hi"', "line\nbreak"],
        ],
      ),
    ).toBe('a,b\r\n"x,y",\r\n"say ""hi""","line\nbreak"\r\n');
  });
});
