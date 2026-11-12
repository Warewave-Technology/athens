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

  it("defuses spreadsheet formulas but keeps numbers", () => {
    expect(
      toCSV(
        ["v"],
        [
          ['=HYPERLINK("http://x")'],
          ["@SUM(A1)"],
          ["+cmd|' /C calc'!A0"],
          ["-2+3"],
          ["-5"],
          ["+1.5e3"],
          ["\tx"],
        ],
      ),
    ).toBe(
      "v\r\n\"'=HYPERLINK(\"\"http://x\"\")\"\r\n'@SUM(A1)\r\n'+cmd|' /C calc'!A0\r\n'-2+3\r\n-5\r\n+1.5e3\r\n'\tx\r\n",
    );
  });
});
