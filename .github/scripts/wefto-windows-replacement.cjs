'use strict';
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const crypto = require('node:crypto');
const {execFileSync, spawnSync} = require('node:child_process');
const repository = path.resolve(__dirname, '../..');
const product = path.join(repository, 'magpie');
const {withHeavyLock} = require(path.join(product, 'scripts/heavy-regression-lock.cjs'));
const {isolatedEnv} = require(path.join(product, 'scripts/product-regression.cjs'));
const baseline = '68c866568959f2f819e118420957442e16620822';
const git = args => execFileSync('git', args, {cwd:repository, encoding:'utf8'}).trim();
withHeavyLock(async () => {
  const output = path.join(product, 'dist/replacement-diagnostic');
  fs.mkdirSync(output, {recursive:true});
  const source = path.join(product, 'internal/steady/steady.go');
  const fixed = fs.readFileSync(source);
  const originalOld = execFileSync('git', ['show', baseline+':magpie/internal/steady/steady.go'], {cwd:repository});
  // The new callers name a one-shot metadata API. In the baseline this is
  // exactly its former os.Stat call, without changing the old I/O behavior.
  const old = Buffer.concat([originalOld, Buffer.from('\nfunc StatOnce(p string) (fs.FileInfo, error) { return stat(p) }\n')]);
  const caches = JSON.parse(execFileSync('go', ['env','-json','GOPATH','GOMODCACHE','GOCACHE'], {cwd:product,encoding:'utf8'}));
  const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'wefto-replacement-diag-'));
  const env = {...isolatedEnv(process.env, temporary, caches), CGO_ENABLED:'1', GOTELEMETRY:'off'};
  const digest = body => crypto.createHash('sha256').update(body).digest('hex');
  const report = {candidate:git(['rev-parse','HEAD']),productTree:git(['rev-parse','HEAD:magpie']),baseline,baselineCompatibility:'StatOnce delegates to the original stat; no new sharing or retries',platform:process.platform,cpus:os.availableParallelism(),go:execFileSync('go',['version'],{env,encoding:'utf8'}).trim(),originalOldSourceSHA256:digest(originalOld),oldSourceSHA256:digest(old),fixedSourceSHA256:digest(fixed),phases:{},status:'failed'};
  const run = (name, count, filter, packages) => {
    const args = ['test','-race','-count='+count,'-p','1','-timeout','15m','-run',filter,'-v',...packages];
    console.log('Running '+name+': go '+args.join(' '));
    const child = spawnSync('go', args, {cwd:product,env,encoding:'utf8',windowsHide:true,maxBuffer:32*1024*1024});
    const log = (child.stdout || '')+(child.stderr || '');
    fs.writeFileSync(path.join(output,name+'.log'), JSON.stringify(args)+'\n'+log);
    process.stdout.write(log);
    if (child.error) throw child.error;
    report.phases[name] = {code:child.status,packages,args};
    return {code:child.status,log};
  };
  try {
    try {
      fs.writeFileSync(source, old);
      const control = run('old-held-reader',1,'^TestReplaceWhileReadingPreviousGeneration$',['./internal/steady']);
      if (control.code !== 1 || !control.log.includes('publishing while an old reader permits replacement') || !control.log.includes('--- FAIL: TestReplaceWhileReadingPreviousGeneration/Rename') || !control.log.includes('--- FAIL: TestReplaceWhileReadingPreviousGeneration/WriteFile')) throw new Error('The old publication did not fail the behavior control as expected');
      const stress = run('old-concurrent-read',20,'^TestReadWhileReplaced$',['./internal/filememo']);
      if (stress.code !== 0 && !(stress.code === 1 && stress.log.includes('the file read as not there'))) throw new Error('Unexpected baseline stress failure');
      report.phases['old-concurrent-read'].reproducedMissing = stress.code === 1;
    } finally { fs.writeFileSync(source,fixed); }
    git(['diff','--exit-code']);
    const result = run('fixed-concurrent-read',20,'^(TestReadWhileReplaced|TestReplaceWhileReadingPreviousGeneration|TestLoginsKeptWhileAnotherMagpieWrites|TestSaveKeepsReadBlockedSettings)$',['./internal/steady','./internal/filememo','./internal/provider','./internal/settings']);
    if (result.code !== 0) throw new Error('Fixed native Windows race comparison failed');
    const boundaries = run('fixed-reader-boundaries',1,'^TestSharedRead',['./internal/steady']);
    if (boundaries.code !== 0) throw new Error('Fixed Windows reader boundary checks failed');
    report.trackedDirty = !!git(['status','--porcelain','--untracked-files=no']);
    if (report.trackedDirty) throw new Error('The fixed source changed during validation');
    report.status = 'passed';
  } finally {
    fs.writeFileSync(source,fixed);
    fs.writeFileSync(path.join(output,'report.json'),JSON.stringify(report,null,2));
    if (!path.isAbsolute(temporary) || path.dirname(temporary)!==path.resolve(os.tmpdir()) || !path.basename(temporary).startsWith('wefto-replacement-diag-')) throw new Error('Unexpected diagnostic cleanup path');
    fs.rmSync(temporary,{recursive:true,force:true});
  }
}).catch(error=>{console.error(error);process.exitCode=1});
