'use strict';
const fs = require('node:fs');
const path = require('node:path');
const os = require('node:os');
const {execFileSync,spawnSync} = require('node:child_process');
const repository = path.resolve(__dirname,'../..');
const product = path.join(repository,'magpie');
const {withHeavyLock} = require(path.join(product,'scripts/heavy-regression-lock.cjs'));
const {isolatedEnv} = require(path.join(product,'scripts/product-regression.cjs'));
const git = args=>execFileSync('git',args,{cwd:repository,encoding:'utf8'}).trim();
withHeavyLock(async()=>{
  const output=path.join(product,'dist/replacement-diagnostic'); fs.mkdirSync(output,{recursive:true});
  const source=path.join(product,'internal/provider/logins.go'); const original=fs.readFileSync(source);
  const old=original.toString();
  const needle='ls, err := filememo.Read("logins", loginsPath(), parseLogins)';
  if(old.split(needle).length!==2) throw new Error('Cannot find the exact account read boundary');
  const injected=old.replace(needle,needle+'\n\tif err != nil || len(ls) == 0 { log.Printf("DIAGNOSTIC account read: count=%d error=%T %v", len(ls), err, err) }');
  const memoSource=path.join(product,'internal/filememo/filememo.go'); const memoOriginal=fs.readFileSync(memoSource);
  const memoNeedle='\tvar fi os.FileInfo\n\tvar err error';
  const memoResult='\tmu.Lock()\n\tif errors.Is(err, fs.ErrNotExist) {';
  const memoOld=memoOriginal.toString();
  if(memoOld.split(memoNeedle).length!==2 || memoOld.split(memoResult).length!==2)throw new Error('Cannot find exact stat boundary');
  const memoInjected=memoOld.replace('"io/fs"','"io/fs"\n "log"\n "path/filepath"').replace(memoNeedle,'\tstarted := time.Now()\n'+memoNeedle).replace(memoResult,`
  if err != nil && filepath.Base(path) == "logins.json" {
    _, probeErr := steady.StatOnce(path)
    root, rootErr := os.OpenRoot(filepath.Dir(path))
    var relativeErr error
    if rootErr == nil { _, relativeErr = root.Stat(filepath.Base(path)); root.Close() }
    log.Printf("DIAGNOSTIC metadata: knownAbsent=%v duration=%s original=%v sharedProbe=%v rootOpen=%v relativeProbe=%v", wasGone, time.Since(started), err, probeErr, rootErr, relativeErr)
  }
`+memoResult);
  const caches=JSON.parse(execFileSync('go',['env','-json','GOPATH','GOMODCACHE','GOCACHE'],{cwd:product,encoding:'utf8'}));
  const temporary=fs.mkdtempSync(path.join(os.tmpdir(),'wefto-account-diag-'));
  const env={...isolatedEnv(process.env,temporary,caches),CGO_ENABLED:'1',GOTELEMETRY:'off'};
  const report={candidate:git(['rev-parse','HEAD']),productTree:git(['rev-parse','HEAD:magpie']),instrumentation:'Log only the count and original read error before account fallback; no credential contents',status:'failed'};
  try{
    fs.writeFileSync(source,injected);
    fs.writeFileSync(memoSource,memoInjected);
    const args=['test','-race','-count=20','-p','1','-timeout','10m','-run','^TestLoginsKeptWhileAnotherMagpieWrites$','-v','./internal/provider'];
    const child=spawnSync('go',args,{cwd:product,env,encoding:'utf8',windowsHide:true,maxBuffer:32*1024*1024});
    const log=(child.stdout||'')+(child.stderr||''); fs.writeFileSync(path.join(output,'account-read.log'),log);process.stdout.write(log);report.code=child.status;
    if(child.error)throw child.error;
    report.status=child.status===0?'passed':'failed'; process.exitCode=child.status;
  }finally{
    fs.writeFileSync(source,original); report.trackedDirty=!!git(['status','--porcelain','--untracked-files=no']);
    fs.writeFileSync(memoSource,memoOriginal); report.trackedDirty=!!git(['status','--porcelain','--untracked-files=no']);
    fs.writeFileSync(path.join(output,'account-report.json'),JSON.stringify(report,null,2));
    if(path.dirname(temporary)!==path.resolve(os.tmpdir())||!path.basename(temporary).startsWith('wefto-account-diag-'))throw new Error('Unexpected temporary path');
    fs.rmSync(temporary,{recursive:true,force:true});
  }
}).catch(error=>{console.error(error);process.exitCode=1});
