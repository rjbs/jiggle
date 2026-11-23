// Drives the page of the editor that jiggle-go serves through a fixed
// script, printing what it saw as one JSON object, for
// t/editor-jmap-browser.t to check.  Playwright is loaded from the directory
// named by JIGGLE_PLAYWRIGHT.  -- claude, 2026-11-23
//
// Its arguments are the server's URL and, if there is one, the id of a
// photo with a real picture, whose renditions can be made again.
import { createRequire } from "node:module";

const require = createRequire(`${process.env.JIGGLE_PLAYWRIGHT}/`);
const { chromium } = require("playwright");

const [url, realPhoto] = process.argv.slice(2);
const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1200, height: 800 } });

const seen = { errors: [], dialogs: [] };
page.on("pageerror", e => seen.errors.push(String(e)));
page.on("dialog", d => { seen.dialogs.push(d.message()); d.accept(); });

const ids   = () => page.$$eval(".thumb", ns => ns.map(n => n.dataset.id));
const query = () => new URL(page.url()).searchParams.get("q");
const status = async () => (await page.locator("#status").textContent()).trim();
const thumb = (id) => page.locator(`.thumb[data-id="${id}"]`);
const badges = (id) => thumb(id).locator(".badge").allTextContents();
const field = (name) => page.locator("#sidebar .field").filter({ has: page.locator(".name", { hasText: new RegExp(`^${name}`) }) });

async function write() {
  await page.keyboard.press("Control+s");
  await page.waitForSelector("#status:has-text('written')");
  return status();
}

async function run(q) {
  await page.locator("#query").fill(q);
  await page.keyboard.press("Enter");
  await page.waitForFunction((q) => document.title === `jiggle: ${q}`, q);
}

// A change made by some other client, which the page has to hear of.
async function elsewhere(id, patch) {
  const res = await fetch(`${url}/jmap/api`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      using: [ "urn:ietf:params:jmap:core", "https://semiotic.systems/jmap/jiggle" ],
      methodCalls: [ [ "Photo/set", { accountId: "photos", update: { [id]: patch } }, "a" ] ],
    }),
  });
  const [ [ , args ] ] = (await res.json()).methodResponses;
  if (!args.updated || !(id in args.updated)) throw new Error(`changing ${id} elsewhere: ${JSON.stringify(args)}`);
}

// The album list.
await page.goto(url);
await page.waitForSelector("#sheet.albums .album");
seen.albums = await page.$$eval(".album .title", ns => ns.map(n => n.textContent));
seen.album_counts = await page.$$eval(".album .counts", ns => ns.map(n => n.textContent));
await page.waitForSelector(".album img");
seen.album_covers = await page.$$eval(".album img", ns => ns.length);

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
seen.album_written = await write();

// Pending photos, two at a time: release the first, write, and refresh.
await run("pending limit:2");
seen.pending_query = query();
seen.pending_first = await ids();
await page.locator(".thumb").first().click();
await field("Visibility").locator("select").selectOption("public");
seen.refresh_disabled_when_dirty = await page.locator("#refresh").isDisabled();
seen.pending_written = await write();
await page.locator("#refresh").click();
await page.waitForFunction((id) => !document.querySelector(`.thumb[data-id="${id}"]`), seen.pending_first[0]);
seen.pending_next = await ids();

// Back returns to the album.
await page.goBack();
await page.waitForFunction(() => document.title.includes("album:"));
seen.back_query = query();

// A query that isn't one is refused, and the batch stays.
await page.locator("#query").fill("pendng");
await page.keyboard.press("Enter");
await page.waitForSelector("#status.bad");
seen.bad_query_status = await status();
seen.after_bad_query = query();

// Tags, one added and one removed, and a new album.
await thumb("aaaa0001").click();
await field("Tags").locator("input").fill("dog");
await page.keyboard.press("Enter");
await field("Tags").locator(".chip", { hasText: "cat" }).locator("button", { hasText: "×" }).click();
await field("Albums").locator("input").fill("Berlin, 2026");
await page.keyboard.press("Enter");
seen.tags_edited = await field("Tags").locator(".chip:not(.removed)").evaluateAll(ns => ns.map(n => n.firstChild.textContent));
seen.tags_written = await write();
seen.albums_after = await field("Albums").locator(".chip").evaluateAll(ns => ns.map(n => n.firstChild.textContent));
seen.dirty_after_write = await page.locator(".thumb.dirty").count();

// A change made elsewhere shows up by itself.
await elsewhere("bbbb0002", { title: "deux" });
await thumb("bbbb0002").locator("figcaption", { hasText: "deux" }).waitFor();
seen.pushed_caption = await thumb("bbbb0002").locator("figcaption").textContent();

// A change made elsewhere to a photo with edits here: the tag added there
// is kept, and the title, which both changed, is a conflict, shown before
// the edit is written over it.
await thumb("cccc0003").click();
await field("Title").locator("input").fill("trois");
await elsewhere("cccc0003", { title: "drei", "tags/elsewhere": true });
await thumb("cccc0003").locator(".badge", { hasText: "changed elsewhere" }).waitFor();
seen.conflict_caption = await thumb("cccc0003").locator("figcaption").textContent();
await thumb("bbbb0002").click();
await thumb("cccc0003").click();
seen.conflict_note = await page.locator("#sidebar .field.invalid .note").first().textContent();
seen.conflict_tags = await field("Tags").locator(".chip").evaluateAll(ns => ns.map(n => n.firstChild.textContent));
seen.conflict_written = await write();
seen.conflict_badges_after = await badges("cccc0003");

// Leaving with unwritten edits asks first.
await thumb("aaaa0001").click();
await field("Title").locator("input").fill("never written");
await page.locator("#albums").click();
await page.waitForSelector("#sheet.albums .album");
seen.albums_after_new = await page.$$eval(".album .title", ns => ns.map(n => n.textContent));

// A photo turned has its pictures made again, and shows them when they're
// done.
if (realPhoto) {
  // A photo whose import failed has no pictures, so it's in no batch, and
  // the page says that it would have been.
  seen.waiting_albums = await page.locator("#waiting").textContent();
  await run("all");
  seen.waiting_all = await page.locator("#waiting").textContent();

  await run(`id:${realPhoto}`);
  const src = () => thumb(realPhoto).locator("img").getAttribute("src");
  const before = await src();
  await thumb(realPhoto).click();
  await field("Extra rotation").locator("button", { hasText: "⟳" }).click();
  seen.turned_written = await write();
  await page.waitForFunction(
    ([id, before]) => document.querySelector(`.thumb[data-id="${id}"] img`)?.getAttribute("src") !== before,
    [realPhoto, before], { timeout: 60000 });
  await page.waitForFunction((id) =>
    ![...document.querySelectorAll(`.thumb[data-id="${id}"] .badge`)].some(n => n.textContent === "importing"), realPhoto);
  seen.turned_badges = await badges(realPhoto);
  seen.turned_transform = await thumb(realPhoto).locator("img").evaluate(n => n.style.transform);
}

console.log(JSON.stringify(seen));
await browser.close();
