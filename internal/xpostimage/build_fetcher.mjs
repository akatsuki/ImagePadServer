import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { build } from 'esbuild'

const directory = new URL('.', import.meta.url)
const license = await readFile(new URL('./react-tweet-MIT.txt', directory), 'utf8')
const licenseComment = [
  '/*! react-tweet 3.3.1 MIT License',
  ...license.trimEnd().split(/\r?\n/).map((line) => ` * ${line}`),
  ' */',
].join('\n')

await build({
  entryPoints: [fileURLToPath(new URL('./fetch_tweet.mjs', directory))],
  outfile: fileURLToPath(new URL('./fetch_tweet.bundle.mjs', directory)),
  bundle: true,
  platform: 'node',
  format: 'esm',
  target: 'node20',
  legalComments: 'eof',
  banner: { js: licenseComment },
})
