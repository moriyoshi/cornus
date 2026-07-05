// Web screenshot helper for the `web-screenshot` skill.
//
// Captures a PNG of a web page with Playwright headless chromium. Intended to be
// run via npx so the driver resolves against the already-cached browser:
//
//   npx --yes -p playwright node shot.mjs --url <url> --out <path.png> [flags]
//
// Flags:
//   --url <url>                 (required) page to capture
//   --out <path>                (required) output PNG path
//   --width <n>                 viewport width  (default 1440)
//   --height <n>                viewport height (default 900)
//   --scale <n>                 deviceScaleFactor, e.g. 2 for retina (default 2)
//   --full-page                 capture the full scroll height, not just viewport
//   --selector <css>            crop to a single element instead of the page
//   --wait <css|ms>             extra wait before shooting: a CSS selector to wait
//                               for, or a millisecond number (default: none;
//                               networkidle is always awaited on navigation)
//   --color-scheme <light|dark> emulate prefers-color-scheme (default: light)
//   --timeout <ms>              navigation/selector timeout (default 30000)

import { createRequire } from 'node:module';
import { pathToFileURL } from 'node:url';

// Resolve `playwright` even when it is not installed next to this script — the
// normal case when invoked via `npx --yes -p playwright node shot.mjs …`, where
// the driver lives in the npx cache. Try a plain import first, then fall back to
// resolving against every node_modules dir implied by PATH (npx puts its temp
// install's .bin on PATH).
function pickChromium(mod) {
  // playwright's entry is CommonJS; `chromium` may land on the namespace or on
  // `default` depending on how Node interops the CJS module.
  return mod.chromium ?? mod.default?.chromium;
}

async function loadChromium() {
  let mod;
  try {
    mod = await import('playwright');
  } catch {
    const require = createRequire(import.meta.url);
    const paths = (process.env.PATH || '')
      .split(':')
      .filter((p) => p.endsWith('node_modules/.bin'))
      .map((p) => p.slice(0, -'/.bin'.length));
    const resolved = require.resolve('playwright', { paths });
    mod = await import(pathToFileURL(resolved).href);
  }
  const chromium = pickChromium(mod);
  if (!chromium) throw new Error('could not load chromium from playwright');
  return chromium;
}

function parseArgs(argv) {
  const opts = {
    width: 1440,
    height: 900,
    scale: 2,
    fullPage: false,
    selector: null,
    wait: null,
    colorScheme: 'light',
    timeout: 30000,
    url: null,
    out: null,
  };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    const next = () => argv[++i];
    switch (a) {
      case '--url': opts.url = next(); break;
      case '--out': opts.out = next(); break;
      case '--width': opts.width = Number(next()); break;
      case '--height': opts.height = Number(next()); break;
      case '--scale': opts.scale = Number(next()); break;
      case '--full-page': opts.fullPage = true; break;
      case '--selector': opts.selector = next(); break;
      case '--wait': opts.wait = next(); break;
      case '--color-scheme': opts.colorScheme = next(); break;
      case '--timeout': opts.timeout = Number(next()); break;
      case '-h':
      case '--help': opts.help = true; break;
      default:
        throw new Error(`unknown argument: ${a}`);
    }
  }
  return opts;
}

function fail(msg) {
  console.error(`shot.mjs: ${msg}`);
  process.exit(1);
}

const opts = parseArgs(process.argv.slice(2));
if (opts.help) {
  console.log('usage: node shot.mjs --url <url> --out <path.png> [--full-page] ' +
    '[--selector <css>] [--width n] [--height n] [--scale n] ' +
    '[--wait <css|ms>] [--color-scheme light|dark] [--timeout ms]');
  process.exit(0);
}
if (!opts.url) fail('--url is required');
if (!opts.out) fail('--out is required');
if (!Number.isFinite(opts.width) || !Number.isFinite(opts.height)) {
  fail('--width/--height must be numbers');
}

let browser;
try {
  const chromium = await loadChromium();
  browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({
    viewport: { width: opts.width, height: opts.height },
    deviceScaleFactor: opts.scale,
    colorScheme: opts.colorScheme,
  });
  const page = await context.newPage();
  await page.goto(opts.url, { waitUntil: 'networkidle', timeout: opts.timeout });

  if (opts.wait != null) {
    if (/^\d+$/.test(opts.wait)) {
      await page.waitForTimeout(Number(opts.wait));
    } else {
      await page.waitForSelector(opts.wait, { timeout: opts.timeout });
    }
  }

  if (opts.selector) {
    const el = page.locator(opts.selector).first();
    await el.waitFor({ state: 'visible', timeout: opts.timeout });
    await el.screenshot({ path: opts.out });
  } else {
    await page.screenshot({ path: opts.out, fullPage: opts.fullPage });
  }

  console.log(`wrote ${opts.out} (${opts.width}x${opts.height}@${opts.scale}x` +
    `${opts.fullPage ? ', full-page' : ''}${opts.selector ? `, element ${opts.selector}` : ''})`);
} catch (err) {
  fail(err && err.message ? err.message : String(err));
} finally {
  if (browser) await browser.close();
}
