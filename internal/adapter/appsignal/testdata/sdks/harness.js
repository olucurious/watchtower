// Synthetic AppSignal Node.js harness: placeholder key, loopback receiver.
const { Appsignal, sendError, setError, setTag, setNamespace, setCustomData, setParams } = require("@appsignal/nodejs")
const appsignal = new Appsignal({
  active: true, name: "library", pushApiKey: process.env.APPSIGNAL_PUSH_API_KEY || "00000000-0000-0000-0000-000000000000",
  environment: "production", revision: "library@synthetic-node", hostname: "matrix-host",
  enableHostMetrics: false, enableMinutelyProbes: false, logPath: "/tmp",
})
const { trace } = require("@opentelemetry/api")

const Catalog = {
  fetch(isbn) { const m = { "978-0": 1 }; if (!(isbn in m)) throw new Error(`key not found: ${isbn}`); return m[isbn] },
  reserve(copies) { if (copies > 3) throw new RangeError(`cannot reserve ${copies} copies; the limit is 3`) },
  deep(n) { if (n === 0) throw new Error("deep failure"); return Catalog.deep(n - 1) + 1 },
}

function step(name, fn) {
  try { fn(); console.log(`STEP ${name} ok`) } catch (e) { console.log(`STEP ${name} raised ${e}`) }
}

step("send_error_with_metadata", () => {
  try { Catalog.fetch("missing") } catch (e) {
    sendError(e, () => {
      setNamespace("background")
      setTag("region", "eu"); setTag("request_id", "req-123")
      setParams({ password: "hunter22", api_token: "tok_live_123" })
    })
  }
})

step("span_error", () => {
  trace.getTracer("harness").startActiveSpan("library.reserve", (span) => {
    try { Catalog.reserve(5) } catch (e) { setNamespace("background_job"); setError(e) }
    span.end()
  })
})

step("error_with_cause", () => {
  try {
    try { Catalog.fetch("missing-cause") } catch (inner) { throw new Error("book could not be reserved", { cause: inner }) }
  } catch (e) { sendError(e) }
})

step("burst_25_errors", () => {
  for (let n = 1; n <= 25; n++) {
    try { Catalog.deep(n % 5) } catch (e) { e.message = `deep failure ${n}`; sendError(e) }
  }
})

setTimeout(async () => { await appsignal.stop(); setTimeout(() => { console.log("HARNESS_DONE node"); process.exit(0) }, 3000) }, 4000)
