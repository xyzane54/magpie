const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const os = require('node:os');
const { spawnSync } = require('node:child_process');
const { runRegression, packages, command, candidateState } = require('../product-regression.cjs');

const root = path.resolve(__dirname, '../..');
test('candidate provenance checks the complete Windows worktree with its index owner from WSL', () => {
  const calls = [];
  const candidate = candidateState({
    platform: 'linux', kernel: '6.6.87.2-microsoft-standard-WSL2',
    gitDirectory: '/mnt/d/repository/.git/worktrees/round3',
    worktree: '/mnt/c/worktrees/round3', gitMarker: 'gitdir: D:/repository/.git/worktrees/round3\n',
    execFileSync: (file, args, options) => {
      calls.push({ file, args, options });
      return args.includes('rev-parse') ? '1234567890abcdef\n' : ' M magpie/internal/routerredact/runtime.go\n';
    }
  });
  assert.equal(candidate.sha, '1234567890abcdef');
  assert.equal(candidate.trackedDirty, true);
  assert.equal(candidate.gitPlatform, 'win32');
  assert.ok(calls.every(call => call.file === 'git.exe'));
  assert.deepEqual(calls[1].args, ['--git-dir=D:/repository/.git/worktrees/round3', '--work-tree=C:/worktrees/round3', 'status', '--porcelain', '--untracked-files=no']);
  assert.equal(calls[1].options.timeout, 60000);
  assert.equal(calls[1].options.env.GIT_OPTIONAL_LOCKS, '0');
});

test('candidate provenance keeps native Git and reports unavailable status as unknown', () => {
  const dependencies = {
    platform: 'linux', kernel: '6.8.0-generic', gitDirectory: '/tmp/repository/.git', worktree: '/tmp/repository', gitMarker: '',
    execFileSync: (file, args) => {
      assert.equal(file, 'git');
      if (args.includes('status')) throw new Error('fixture timeout');
      return '1234567890abcdef\n';
    }
  };
  const unavailable = candidateState(dependencies);
  assert.equal(unavailable.sha, '1234567890abcdef');
  assert.equal(unavailable.trackedDirty, null);
  assert.match(unavailable.reason, /unavailable or timed out/);
  assert.equal(unavailable.gitPlatform, 'linux');
  const clean = candidateState({ ...dependencies, execFileSync: (_file, args) => args.includes('status') ? '' : '1234567890abcdef\n' });
  assert.equal(clean.trackedDirty, false);
});

test('candidate provenance uses native Git on Windows and never converts a Git error to clean', () => {
  const dependencies = {
    platform: 'win32', kernel: '10.0.26100', gitDirectory: 'D:/repository/.git/worktrees/round3',
    worktree: 'C:/worktrees/round3', gitMarker: 'gitdir: D:/repository/.git/worktrees/round3\n',
    execFileSync: (file, args) => {
      assert.equal(file, 'git');
      assert.ok(args.includes('--git-dir=D:/repository/.git/worktrees/round3'));
      assert.ok(args.includes('--work-tree=C:/worktrees/round3'));
      return args.includes('status') ? '' : '1234567890abcdef\n';
    }
  };
  const native = candidateState(dependencies);
  assert.equal(native.gitPlatform, 'win32');
  assert.equal(native.gitExecutable, 'git');
  assert.equal(native.trackedDirty, false);
  const unavailable = candidateState({ ...dependencies, execFileSync: () => { throw new Error('Git absent'); } });
  assert.equal(unavailable.sha, null);
  assert.equal(unavailable.trackedDirty, undefined);
  assert.equal(unavailable.reason, 'Git metadata unavailable');
});

test('Make exposes a unified product regression command', () => {
  const makefile = fs.readFileSync(path.join(root, 'Makefile'), 'utf8');
  assert.ok(/^test-product:\r?\n\tnode scripts\/product-regression\.cjs$/m.test(makefile),
    'make test-product must run the product regression entry, including routerservice pages');
});

function fixture(t) {
  fs.mkdirSync(path.join(root, 'dist'), { recursive: true });
  const directory = fs.mkdtempSync(path.join(root, 'dist', 'entry-test-'));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const calls = [];
  return {
    options: { output: path.join(directory, 'output'), env: {
      ...process.env, CODEX_HOME: 'DO-NOT-READ', OPENAI_API_KEY: 'DO-NOT-SEND',
      HTTPS_PROXY: 'DO-NOT-USE', NODE_OPTIONS: 'DO-NOT-INHERIT', GOOS: 'foreign',
      API_ROUTER_BINARY: 'stale-binary', BROWSER: 'chromium', PLAYWRIGHT_HOST_PLATFORM_OVERRIDE: 'ubuntu24.04-x64'
    } }, calls,
    dependencies: {
      goEnvironment: () => ({ GOVERSION: 'go1.26.8', GOPATH: directory, GOMODCACHE: directory, GOCACHE: directory }),
      browserPreflight: async (engine, env) => { calls.push({ preflight: engine, env }); },
      command: async (file, args, env, log) => { calls.push({ file, args, env, log }); }
    }
  };
}

test('default gate retains all product packages and ten pages, including onboarding and acceptance checks', async t => {
  const f = fixture(t);
  const { code, summary } = await runRegression(f.options, f.dependencies);
  assert.equal(code, 0); assert.equal(summary.status, 'passed'); assert.equal(summary.complete, true);
  assert.deepEqual(summary.steps.map(step => step.status), Array(5).fill('passed'));
  const commands = f.calls.filter(call => call.args);
  assert.equal(commands.length, 5);
  assert.equal(packages.length, 26);
  assert.ok(packages.includes('./internal/routerredact'));
  assert.ok(packages.includes('./cmd/model-fetch'));
  assert.ok(packages.includes('./cmd/real-model-check'));
  assert.ok(packages.includes('./internal/routeracceptance'));
  for (const verb of ['test', 'vet']) {
    const call = commands.find(call => call.args[0] === verb);
    assert.deepEqual(call.args.filter(arg => arg.startsWith('./')), packages);
  }
  assert.equal(commands[1].env.CGO_ENABLED, '1');
  assert.ok(commands[1].args.includes('-race')); assert.ok(commands[1].args.includes('-count=1'));
  for (const engine of ['chromium', 'webkit']) {
    const call = commands.find(call => call.env.BROWSER === engine);
    assert.equal(summary.browserFiles.length, 10);
    assert.ok(summary.browserFiles.includes('internal/routerservice/tests/tutorial.test.cjs'));
    assert.ok(summary.browserFiles.includes('internal/routerservice/tests/spark-models.test.cjs'));
    assert.ok(summary.browserFiles.includes('internal/routerservice/tests/redaction.test.cjs'));
    assert.ok(summary.browserFiles.includes('internal/routerservice/tests/loomy-host.test.cjs'));
    assert.ok(summary.browserFiles.includes('internal/routerservice/tests/local-model.test.cjs'));
    assert.deepEqual(call.args.filter(arg => arg.endsWith('.test.cjs')), summary.browserFiles);
    assert.ok(call.args.includes('--test-concurrency=1'));
    assert.ok(path.isAbsolute(call.env.API_ROUTER_BINARY));
  }
  const env = commands[0].env;
  for (const key of ['CODEX_HOME', 'OPENAI_API_KEY', 'HTTPS_PROXY', 'NODE_OPTIONS', 'GOOS']) assert.equal(env[key], undefined, key);
  assert.equal(env.PLAYWRIGHT_HOST_PLATFORM_OVERRIDE, 'ubuntu24.04-x64');
  assert.equal(env.GOPROXY, 'off'); assert.equal(env.GOTOOLCHAIN, 'local');
  for (const key of ['HOME', 'USERPROFILE', 'XDG_CONFIG_HOME', 'XDG_CACHE_HOME', 'XDG_DATA_HOME', 'XDG_STATE_HOME', 'APPDATA', 'LOCALAPPDATA', 'TMP', 'TEMP', 'TMPDIR']) {
    assert.ok(env[key].startsWith(summary.temporary), key);
  }
  assert.equal(fs.existsSync(summary.temporary), false);
  assert.equal(JSON.parse(fs.readFileSync(path.join(summary.output, 'summary.json'))).status, 'passed');
});

for (const failure of ['build', 'race', 'vet', 'chromium', 'webkit']) {
  test(`${failure} failure returns nonzero and blocks subsequent commands`, async t => {
    const f = fixture(t);
    const names = ['build', 'race', 'vet', 'chromium', 'webkit'];
    let count = 0;
    f.dependencies.command = async () => {
      const name = names[count++]; if (name === failure) throw new Error(`injected ${name} failure`);
    };
    const { code, summary } = await runRegression(f.options, f.dependencies);
    assert.equal(code, 1); assert.equal(summary.status, 'failed'); assert.equal(summary.complete, false);
    assert.equal(summary.steps[names.indexOf(failure)].status, 'failed');
    assert.equal(count, names.indexOf(failure) + 1);
    for (const step of summary.steps.slice(count)) {
      assert.equal(step.status, 'not_run'); assert.match(step.reason, /blocked/);
    }
    assert.match(fs.readFileSync(summary.steps[count - 1].log, 'utf8'), /FAILED/);
  });
}

test('missing browser is failure, not a successful skip', async t => {
  const f = fixture(t);
  f.dependencies.browserPreflight = async () => { throw new Error('webkit dependency unavailable'); };
  const { code, summary } = await runRegression({ ...f.options, only: 'webkit' }, f.dependencies);
  assert.equal(code, 1); assert.equal(summary.steps[4].status, 'failed');
  assert.match(summary.steps[4].reason, /unavailable/);
  assert.equal(f.calls.length, 1, 'only the build ran; page tests must not start without the browser');
});

test('diagnostic selection cannot report a full product pass', async t => {
  const f = fixture(t);
  const { code, summary } = await runRegression({ ...f.options, only: 'race' }, f.dependencies);
  assert.equal(code, 0); assert.equal(summary.complete, false); assert.equal(summary.status, 'partial');
  assert.equal(summary.steps[0].status, 'not_run');
  assert.match(summary.steps[0].reason, /not requested/);
});

test('runner uses the native temporary filesystem for service and SQLite data', async t => {
  const f = fixture(t);
  const { summary } = await runRegression({ ...f.options, only: 'build' }, f.dependencies);
  assert.equal(path.dirname(summary.temporary), os.tmpdir(), 'temporary service data must not follow a checkout on a mounted filesystem');
  assert.equal(fs.existsSync(summary.temporary), false);
});

test('unavailable Go produces a failed summary and starts no command', async t => {
  const f = fixture(t);
  f.dependencies.goEnvironment = () => { throw new Error('Go unavailable'); };
  const { code, summary } = await runRegression(f.options, f.dependencies);
  assert.equal(code, 1); assert.equal(f.calls.length, 0); assert.equal(summary.status, 'failed');
  assert.ok(summary.steps.every(step => step.status === 'not_run' && step.reason.includes('Go unavailable')));
});

test('CLI really exits nonzero when its Go executable is absent', t => {
  const f = fixture(t);
  const result = spawnSync(process.execPath, [path.join(root, 'scripts/product-regression.cjs'), `--output=${f.options.output}`], {
    cwd: root, env: { ...process.env, PATH: path.dirname(f.options.output) }, encoding: 'utf8', windowsHide: true
  });
  assert.equal(result.status, 1, result.stderr);
  const summary = JSON.parse(fs.readFileSync(path.join(f.options.output, 'summary.json')));
  assert.equal(summary.status, 'failed'); assert.equal(summary.complete, false);
  assert.match(summary.error, /Go unavailable/);
});

test('a real failing child preserves its error output and rejects', async t => {
  const f = fixture(t);
  const log = path.join(path.dirname(f.options.output), 'child.log');
  await assert.rejects(command(process.execPath, ['-e', 'console.error("fixture failed");process.exit(23)'], {}, log), /exited 23/);
  assert.match(fs.readFileSync(log, 'utf8'), /fixture failed/);
});
const {withHeavyLock}=require('../heavy-regression-lock.cjs');
test('heavy lock serializes callers and releases after callback failure',async t=>{
 const dir=fs.mkdtempSync(path.join(os.tmpdir(),'router-lock-test-'));t.after(()=>fs.rmSync(dir,{recursive:true,force:true}));const lock=path.join(dir,'lock');const order=[];
 let release;const held=new Promise(r=>{release=r});let entered;const ready=new Promise(r=>{entered=r});
 const first=withHeavyLock(async()=>{order.push('first');entered();await held;order.push('released');},lock);await ready;
 const second=withHeavyLock(async()=>{order.push('second');throw Error('fixture failure');},lock);
 release();await first;await assert.rejects(second,/fixture failure/);assert.deepEqual(order,['first','released','second']);assert.equal(fs.existsSync(lock),false);
});
test('heavy lock never removes a changed owner',async t=>{
 const dir=fs.mkdtempSync(path.join(os.tmpdir(),'router-lock-owner-'));t.after(()=>fs.rmSync(dir,{recursive:true,force:true}));const lock=path.join(dir,'lock');
 await assert.rejects(withHeavyLock(async()=>fs.writeFileSync(path.join(lock,'owner.json'),JSON.stringify({id:'other'})),lock),/ownership changed/);assert.equal(fs.existsSync(lock),true);
});
test('interrupted lock remains owned for descendant cleanup and cannot report pass',async t=>{
 const dir=fs.mkdtempSync(path.join(os.tmpdir(),'router-lock-interrupt-'));t.after(()=>fs.rmSync(dir,{recursive:true,force:true}));const lock=path.join(dir,'lock');
 await assert.rejects(withHeavyLock(async control=>{process.emit('SIGTERM');assert.equal(control.interrupted(),true)},lock),/interrupted/);assert.equal(fs.existsSync(path.join(lock,'owner.json')),true);
 const f=fixture(t);let stopped=false;f.dependencies.command=async()=>{stopped=true};const result=await runRegression({...f.options,only:'build',lockControl:{interrupted:()=>stopped}},f.dependencies);assert.equal(result.summary.status,'failed');assert.equal(result.summary.complete,false);
});
