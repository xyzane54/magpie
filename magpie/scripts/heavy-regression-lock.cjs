'use strict';
// One machine-wide directory lock, shared by all worktrees and Windows/WSL.
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const {spawn} = require('node:child_process');
function hostPath(value) {
  if (process.platform !== 'win32' && /^[A-Za-z]:[\\/]/.test(value)) return '/mnt/' + value[0].toLowerCase() + value.slice(2).replaceAll('\\', '/');
  return value;
}
function repositoryGitDir() {
  const checkout = path.resolve(__dirname, '../..');
  const marker = path.join(checkout, '.git');
  let directory = marker;
  if (fs.statSync(marker).isFile()) {
    const gitdir = fs.readFileSync(marker, 'utf8').trim().replace(/^gitdir: /, '');
    directory = path.resolve(checkout, hostPath(gitdir));
  }
  return directory;
}
function defaultLock() {
  const directory = repositoryGitDir();
  const marker = path.join(directory, 'commondir');
  const common = fs.existsSync(marker) ? path.resolve(directory, hostPath(fs.readFileSync(marker,'utf8').trim())) : directory;
  return path.join(path.dirname(common), '.test-locks', 'heavy-regression.lock');
}
async function withHeavyLock(run, lock = defaultLock()) {
  fs.mkdirSync(path.dirname(lock), {recursive:true});
  const id = crypto.randomUUID();
  let interrupted = false;
  const signal = () => { interrupted = true; };
  process.on('SIGINT', signal); process.on('SIGTERM', signal);
  let acquired = false;
  try {
    let announced = false;
    while (!acquired) {
      if (interrupted) throw new Error('Interrupted while waiting for heavy regression lock');
      try { fs.mkdirSync(lock); acquired = true; }
      catch (error) {
        if (error.code !== 'EEXIST') throw error;
        if (!announced) { console.log(`Waiting for heavy regression lock: ${lock}`); announced = true; }
        await new Promise(resolve => setTimeout(resolve, 1000));
      }
    }
    const owner = path.join(lock, 'owner.json');
    fs.writeFileSync(owner, JSON.stringify({id,pid:process.pid,platform:process.platform,started:new Date().toISOString(),cwd:process.cwd()}));
    const result = await run({lock, interrupted: () => interrupted});
    if (interrupted) throw new Error('Heavy regression interrupted; lock retained for descendant cleanup');
    return result;
  } finally {
    process.removeListener('SIGINT', signal); process.removeListener('SIGTERM', signal);
    if (acquired && interrupted) {
      console.error(`Interrupted: retaining heavy lock until owned descendants are confirmed stopped: ${lock}`);
    } else if (acquired) {
      const owner = path.join(lock, 'owner.json');
      if (!fs.existsSync(owner)) fs.rmdirSync(lock);
      else if (JSON.parse(fs.readFileSync(owner,'utf8')).id === id) { fs.unlinkSync(owner); fs.rmdirSync(lock); }
      else throw new Error('Heavy regression lock ownership changed; refusing release');
    }
  }
}
async function runLockedCommand(command, args) {
  // The product gate owns its lock; historical wrappers must not lock it twice.
  const script = args[0] ? path.resolve(args[0]) : '';
  const ownsLock = /^node(?:js)?(?:\.exe)?$/i.test(path.basename(command)) && [
    path.resolve(__dirname,'product-regression.cjs'),
    ...['round2-loomy','round2-workbuddy'].map(name=>path.resolve(__dirname,'../../z-steps',name,'run-locked.cjs'))
  ].includes(script);
  const run = () => new Promise((resolve,reject) => {
    const child = spawn(command,args,{stdio:'inherit',windowsHide:true});
    const stop = signal => child.kill(signal);
    const int = () => stop('SIGINT'), term = () => stop('SIGTERM');
    process.on('SIGINT',int); process.on('SIGTERM',term);
    const cleanup = () => { process.removeListener('SIGINT',int);process.removeListener('SIGTERM',term); };
    child.once('error',error => { cleanup();reject(error); });
    child.once('close',code => { cleanup();resolve(code ?? 1); });
  });
  return ownsLock ? run() : withHeavyLock(run);
}
module.exports = {withHeavyLock,defaultLock,repositoryGitDir,runLockedCommand};
