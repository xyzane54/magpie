'use strict';
const fs=require('node:fs'), path=require('node:path'), os=require('node:os');
const {execFileSync,spawnSync}=require('node:child_process');
const repository=path.resolve(__dirname,'../..'), product=path.join(repository,'magpie');
const {withHeavyLock}=require(path.join(product,'scripts/heavy-regression-lock.cjs'));
const {isolatedEnv}=require(path.join(product,'scripts/product-regression.cjs'));
const git=args=>execFileSync('git',args,{cwd:repository,encoding:'utf8'}).trim();
withHeavyLock(async()=>{
  const output=path.join(product,'dist/replacement-diagnostic');fs.mkdirSync(output,{recursive:true});
  const originals=new Map();
  const edit=(name,transform)=>{const file=path.join(product,name), original=fs.readFileSync(file);originals.set(file,original);fs.writeFileSync(file,transform(original.toString()));};
  const replace=(source,needle,replacement)=>{if(source.split(needle).length!==2)throw new Error('Ambiguous diagnostic boundary: '+needle);return source.replace(needle,replacement);};
  const caches=JSON.parse(execFileSync('go',['env','-json','GOPATH','GOMODCACHE','GOCACHE'],{cwd:product,encoding:'utf8'}));
  const temporary=fs.mkdtempSync(path.join(os.tmpdir(),'wefto-account-diag-'));
  const env={...isolatedEnv(process.env,temporary,caches),CGO_ENABLED:'1',GOTELEMETRY:'off'};
  const report={candidate:git(['rev-parse','HEAD']),productTree:git(['rev-parse','HEAD:magpie']),instrumentation:'Count native and direct publication, then stop writers only after the original account-empty assertion fires; log only errors, paths, filenames and sizes',status:'failed'};
  try{
    edit('internal/steady/rename_windows.go',source=>{
      source=replace(source,'"path/filepath"','"path/filepath"\n "sync/atomic"\n "log"');
      source=source.replaceAll('return os.Rename(oldpath, newpath)','diagnosticDirect.Add(1); return os.Rename(oldpath, newpath)');
      source=replace(source,'\troot, err := os.OpenRoot(directory)','\tif diagnosticRoots.Add(1) <= 2 { log.Printf("DIAGNOSTIC sibling publisher: old=%q new=%q directory=%q", oldpath, newpath, directory) }\n\troot, err := os.OpenRoot(directory)');
      return source+'\nvar diagnosticRoots, diagnosticDirect atomic.Uint64\nfunc DiagnosticRenames() (uint64,uint64) { return diagnosticRoots.Load(), diagnosticDirect.Load() }\n';
    });
    edit('internal/provider/logins_windows_test.go',source=>{
      source=replace(source,'\tvar stop atomic.Bool','\tvar renamed, renameFailed atomic.Uint64\n\tvar stop atomic.Bool');
      source=replace(source,'if steady.Rename(tmp.Name(), p) != nil {\n\t\t\t\t\tos.Remove(tmp.Name())\n\t\t\t\t}',`if renameErr := steady.Rename(tmp.Name(), p); renameErr != nil {
          if renameFailed.Add(1) <= 3 { t.Logf("DIAGNOSTIC publication failure: %v", renameErr) }
          os.Remove(tmp.Name())
        } else { renamed.Add(1) }`);
      return replace(source,'\t\t\tmisses++',`\t\t\tmisses++
      stop.Store(true); <-done; <-done
      entries, listErr := os.ReadDir(filepath.Dir(p)); var names []string
      for _, entry := range entries { names=append(names,entry.Name()) }
      info, statErr := steady.Stat(p); var size int64=-1
      if info!=nil { size=info.Size() }
      body, readErr := steady.ReadThere(p)
      roots, direct := steady.DiagnosticRenames()
      loginsMu.Unlock()
      t.Fatalf("DIAGNOSTIC original empty-account failure: successful=%d failed=%d root=%d direct=%d afterStop stat=%v size=%d read=%v bytes=%d directory=%q listError=%v",renamed.Load(),renameFailed.Load(),roots,direct,statErr,size,readErr,len(body),names,listErr)
`);
    });
    const args=['test','-race','-count=20','-p','1','-timeout','10m','-run','^TestLoginsKeptWhileAnotherMagpieWrites$','-v','./internal/provider'];
    const child=spawnSync('go',args,{cwd:product,env,encoding:'utf8',windowsHide:true,maxBuffer:32*1024*1024});
    const log=(child.stdout||'')+(child.stderr||'');fs.writeFileSync(path.join(output,'account-read.log'),log);process.stdout.write(log);report.code=child.status;
    if(child.error)throw child.error;
    report.status=child.status===0?'passed':'failed';process.exitCode=child.status;
  }finally{
    for(const [file,body] of originals)fs.writeFileSync(file,body);
    report.trackedDirty=!!git(['status','--porcelain','--untracked-files=no']);
    fs.writeFileSync(path.join(output,'account-report.json'),JSON.stringify(report,null,2));
    if(path.dirname(temporary)!==path.resolve(os.tmpdir())||!path.basename(temporary).startsWith('wefto-account-diag-'))throw new Error('Unexpected temporary path');
    fs.rmSync(temporary,{recursive:true,force:true});
  }
}).catch(error=>{console.error(error);process.exitCode=1});
