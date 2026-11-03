declare module "node:test" {
  function test(name: string, fn: () => void | Promise<void>): Promise<void>;
  export default test;
}

declare module "node:assert/strict" {
  const assert: {
    equal(actual: unknown, expected: unknown, message?: string): void;
    notEqual(actual: unknown, expected: unknown, message?: string): void;
    deepEqual(actual: unknown, expected: unknown, message?: string): void;
    throws(block: () => unknown, error?: RegExp | ErrorConstructor, message?: string): void;
  };
  export default assert;
}
