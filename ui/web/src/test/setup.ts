import { beforeEach } from "vitest";

// Vitest exposes the original jsdom instance separately from its Node globals.
// Node's native web storage may otherwise shadow the browser storage.
const browser = (globalThis as unknown as {
  jsdom: { window: Window };
}).jsdom.window;
for (const name of ["localStorage", "sessionStorage"] as const) {
  Object.defineProperty(globalThis, name, {
    configurable: true,
    value: browser[name],
  });
}
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
});
