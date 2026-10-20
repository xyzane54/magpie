#!/usr/bin/env node
'use strict';

// Export the current source tree without copying development Git history.
// Run from any directory; only built-in Node modules and Git are needed.
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const { execFileSync, spawnSync } = require('node:child_process');

const source = path.resolve(__dirname, '..');
const args = process.argv.slice(2);
const outputArgument = args.find(arg => arg.startsWith('--output='));
if (args.includes('--help')) {
  console.log('node scripts/prepare-public-source.cjs --output=<new-directory> [--init-git]');
  console.log('Copies current public source, including uncommitted changes. Never pushes or commits.');
  process.exit(0);
}
if (!outputArgument || args.some(arg => arg !== outputArgument && arg !== '--init-git')) {
  throw new Error('Expected --output=<new-directory> and optional --init-git; see --help.');
}
const output = path.resolve(outputArgument.slice('--output='.length));
if (fs.existsSync(output)) throw new Error('Output must be a new directory; existing files are never overwritten.');
const report = output + '.manifest.json';
if (fs.existsSync(report)) throw new Error('Manifest path already exists; choose a new output directory.');
const relativeOutput = path.relative(source, output);
if (!relativeOutput || (!relativeOutput.startsWith('..' + path.sep) && !path.isAbsolute(relativeOutput) && relativeOutput.split(path.sep)[0] !== 'dist')) {
  throw new Error('Use a directory outside the source checkout or under its ignored dist/ directory.');
}

function git(command, input) {
  return execFileSync('git', ['-C', source, ...command], {
    input, windowsHide: true, maxBuffer: 32 * 1024 * 1024,
    env: { ...process.env, GIT_OPTIONAL_LOCKS: '0' }
  });
}
function splitPaths(buffer) {
  return buffer.toString('utf8').split('\0').filter(Boolean);
}
const actualRoot = fs.realpathSync(git(['rev-parse', '--show-toplevel']).toString('utf8').trim());
if (actualRoot !== fs.realpathSync(source)) throw new Error('The script must be inside the source repository root.');
const files = [...new Set(splitPaths(git(['ls-files', '--cached', '--others', '--exclude-standard', '-z'])))].sort();
// --no-index also evaluates ignore rules for already tracked private records.
const ignoredResult = spawnSync('git', ['-C', source, 'check-ignore', '--no-index', '--stdin', '-z'], {
  input: Buffer.from(files.join('\0') + '\0'), windowsHide: true,
  maxBuffer: 32 * 1024 * 1024, env: { ...process.env, GIT_OPTIONAL_LOCKS: '0' }
});
if (ignoredResult.error) throw ignoredResult.error;
if (![0, 1].includes(ignoredResult.status)) throw new Error('Git ignore evaluation failed: ' + ignoredResult.stderr.toString());
const ignored = new Set(splitPaths(ignoredResult.stdout));
const rootFiles = new Set(['README.md', 'LICENSE', 'VERSION', '.gitignore', '.gitattributes']);
function isPublic(name) {
  return rootFiles.has(name) || ['magpie/', 'scripts/', '.github/'].some(prefix => name.startsWith(prefix));
}
const included = [], excluded = [];
for (const name of files) {
  if (ignored.has(name) || !isPublic(name)) { excluded.push(name); continue; }
  const from = path.resolve(source, name);
  if (path.relative(source, from).startsWith('..' + path.sep) || path.isAbsolute(path.relative(source, from))) {
    throw new Error('Source path escapes checkout: ' + name);
  }
  if (!fs.existsSync(from)) continue; // A tracked file deleted in the working tree.
  // Reject symlinks/junctions anywhere in a copied path instead of following
  // them into private files outside the source tree.
  let cursor = from;
  while (cursor !== source) {
    if (fs.lstatSync(cursor).isSymbolicLink()) throw new Error('Review linked source before publishing: ' + name);
    cursor = path.dirname(cursor);
  }
  if (!fs.statSync(from).isFile()) throw new Error('Expected a regular source file: ' + name);
  included.push(name);
}
for (const required of ['README.md', 'LICENSE', 'VERSION', '.gitignore', '.gitattributes', 'magpie/go.mod', 'magpie/go.sum', 'magpie/package-lock.json']) {
  if (!included.includes(required)) throw new Error('Required source file excluded or missing: ' + required);
}

fs.mkdirSync(output, { recursive: true });
console.log(`Copying ${included.length} public source files...`);
let bytes = 0;
const manifest = [];
for (const name of included) {
  const from = path.join(source, name), to = path.join(output, name);
  fs.mkdirSync(path.dirname(to), { recursive: true });
  const content = fs.readFileSync(from);
  fs.writeFileSync(to, content, { mode: fs.statSync(from).mode & 0o777 });
  bytes += content.length;
  manifest.push({ path: name, bytes: content.length, sha256: crypto.createHash('sha256').update(content).digest('hex') });
}
if (args.includes('--init-git')) {
  console.log('Initializing an independent main branch and staging source files...');
  execFileSync('git', ['init', '--initial-branch=main', output], { windowsHide: true, stdio: 'pipe' });
  execFileSync('git', ['-C', output, 'add', '--all'], { windowsHide: true, stdio: 'pipe' });
  // Core Git only preserves executable mode if the source index supplies it
  // on Windows. Restore those bits for checked-in shell scripts.
  const executable = git(['ls-files', '--stage', '-z']);
  for (const entry of splitPaths(executable)) {
    const match = /^100755 [^\t]+\t(.+)$/.exec(entry);
    if (match && included.includes(match[1])) {
      execFileSync('git', ['-C', output, 'update-index', '--chmod=+x', '--', match[1]], { windowsHide: true, stdio: 'pipe' });
    }
  }
}
fs.writeFileSync(report, JSON.stringify({ files: included.length, bytes, excluded, manifest }, null, 2) + '\n');
console.log(JSON.stringify({ output, manifest: report, files: included.length, MiB: +(bytes / 1048576).toFixed(2), excluded: excluded.length, gitInitialized: args.includes('--init-git'), committed: false, pushed: false }, null, 2));
