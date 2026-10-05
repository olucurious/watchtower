// Front-end harness: bundled into the page, reports to the local receiver.
import Appsignal from "@appsignal/javascript"
// ?key=…&uri=… override the placeholder key and the local receiver.
const params = new URLSearchParams(location.search)
const appsignal = new Appsignal({
  key: params.get("key") || "00000000-0000-0000-0000-000000000000",
  uri: params.get("uri") || location.origin + "/collect",
  revision: "library@synthetic-frontend",
  namespace: "frontend",
})

function loadMember(member) { if (!member.id) throw new TypeError("member id missing") }
function reserve() { try { loadMember({}) } catch (e) { throw new Error("reservation failed", { cause: e }) } }

window.run = async () => {
  try { reserve() } catch (e) {
    await appsignal.sendError(e, (span) => {
      span.setAction("BookPage#reserve")
      span.setTags({ region: "eu", request_id: "req-123" })
      span.setParams({ password: "hunter22" })
    })
  }
  try { JSON.parse("{not json") } catch (e) { await appsignal.sendError(e) }
  return "sent"
}
