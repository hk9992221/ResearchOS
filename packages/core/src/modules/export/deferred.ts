/**
 * Minimal typed deferred. The workspace tsconfig targets ES2022, so
 * `Promise.withResolvers` is unavailable; this keeps the resolver pair in one
 * named place instead of open-coding it at each I/O boundary.
 */
export interface Deferred<T> {
  promise: Promise<T>
  resolve(value: T | PromiseLike<T>): void
  reject(reason?: unknown): void
}

export function deferred<T>(): Deferred<T> {
  let resolve!: (value: T | PromiseLike<T>) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

export function deferredVoid(): Deferred<void> {
  return deferred<void>()
}