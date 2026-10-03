// Drives the editor's page through a fixed script, printing what it saw as
// one JSON object, for t/editor-browser.t to check.  Playwright is loaded
// from the directory named by JIGGLE_PLAYWRIGHT.  -- claude, 2026-10-03
import { createRequire } from "node:module";

const require = createRequire(`${process.env.JIGGLE_PLAYWRIGHT}/`);
const { chromium } = require("playwright");

const [url] = process.argv.slice(2);
const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1200, height: 800 } });

const seen = { errors: [] };
page.on("pageerror", e => seen.errors.push(String(e)));
page.on("dialog", d => { seen.dialog = d.message(); d.accept(); });

const ids   = () => page.$$eval(".thumb", ns => ns.map(n => n.dataset.id));
const query = () => new URL(page.url()).searchParams.get("q");
const written = () => page.waitForSelector("#status:has-text('committed')");

// The album list.
await page.goto(url);
await page.waitForSelector("#sheet.albums .album");
seen.albums = await page.$$eval(".album .title", ns => ns.map(n => n.textContent));
seen.album_counts = await page.$$eval(".album .counts", ns => ns.map(n => n.textContent));

// An album, in album mode: move the last photo to the start, make it the
// cover, and write.
await page.locator(".album", { hasText: "Trip" }).click();
await page.waitForSelector(".thumb");
seen.album_query = query();
seen.album_order = await ids();
await page.locator(".thumb").last().click();
await page.locator("#sidebar button", { hasText: "Move to start" }).click();
await page.locator("#sidebar button", { hasText: "Make cover" }).click();
seen.album_order_edited = await ids();
seen.album_write_button = (await page.locator("#write").textContent()).trim();
await page.keyboard.press("Meta+s");
await written();

// Pending photos, two at a time: release the first, write, and refresh.
await page.locator("#query").fill("pending limit:2");
await page.keyboard.press("Enter");
await page.waitForFunction(() => document.title.includes("limit:2"));
seen.pending_query = query();
seen.pending_first = await ids();
await page.locator(".thumb").first().click();
await page.locator("#sidebar .field", { hasText: "Pending" }).locator("input[type=checkbox]").uncheck();
seen.refresh_disabled_when_dirty = await page.locator("#refresh").isDisabled();
await page.keyboard.press("Meta+s");
await written();
await page.locator("#refresh").click();
await page.waitForFunction((n) => !document.querySelector(`.thumb[data-id="${n}"]`), seen.pending_first[0]);
seen.pending_next = await ids();

// Back returns to the album.
await page.goBack();
await page.waitForFunction(() => document.title.includes("album:"));
seen.back_query = query();

// A bad query is reported, and changes nothing.
await page.locator("#query").fill("pendng");
await page.keyboard.press("Enter");
await page.waitForSelector("#status.bad");
seen.bad_query_status = (await page.locator("#status").textContent()).trim();
seen.after_bad_query = query();

// Leaving unwritten changes asks first.
await page.locator(".thumb").first().click();
await page.locator("#sidebar .field", { hasText: "Title" }).locator("input").first().fill("unwritten");
await page.locator("#albums").click();
await page.waitForSelector("#sheet.albums .album");

console.log(JSON.stringify(seen));
await browser.close();
