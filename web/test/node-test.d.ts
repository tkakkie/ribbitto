declare module "node:test" {
  function test(name: string, fn: () => void | Promise<void>): Promise<void>;
  export default test;
}

declare module "node:assert/strict" {
  const assert: {
    equal(actual: unknown, expected: unknown, message?: string): void;
    notEqual(actual: unknown, expected: unknown, message?: string): void;
    deepEqual(actual: unknown, expected: unknown, message?: string): void;
    doesNotMatch(value: string, pattern: RegExp, message?: string): void;
    throws(block: () => unknown, error?: RegExp | ErrorConstructor, message?: string): void;
  };
  export default assert;
}

declare module "node:fs" {
  export function readFileSync(path: URL, encoding: "utf8"): string;
}
