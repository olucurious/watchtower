import { chromium } from "playwright-core"
const browser = await chromium.launch({ executablePath: process.env.CHROME })
const page = await browser.newPage()
const blocked = []
await page.route("**/*", (route) => {
  const u = new URL(route.request().url())
  if (u.hostname === "127.0.0.1") return route.continue()
  blocked.push(u.href); return route.abort()
})
await page.goto("http://127.0.0.1:18905/")
console.log(await page.evaluate(() => window.run()))
await page.waitForTimeout(1500)
console.log("blocked external requests:", blocked.length ? blocked : "none")
await browser.close()
