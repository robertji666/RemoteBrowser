// Observe revocation of an already-connected disposable Bob WebRTC peer.
// No browser automation, password changes, instance changes or deletions.
// Bob is re-enabled in finally, including when an assertion fails.
import assert from 'node:assert/strict';
import {execFileSync} from 'node:child_process';
import {createHash} from 'node:crypto';
import {mkdir,readFile,writeFile} from 'node:fs/promises';
import {join} from 'node:path';

const origin=process.env.RB_E2E_URL||'http://localhost';
assert(['localhost','127.0.0.1'].includes(new URL(origin).hostname),'local test target required');
const docker=(...args)=>execFileSync('docker',args,{encoding:'utf8',timeout:15000,stdio:['ignore','pipe','pipe']}).trim();
const inspect=id=>JSON.parse(docker('inspect','rb-sess-'+id))[0];
const stats=id=>JSON.parse(docker('exec','rb-sess-'+id,'curl','-fsS','--max-time','2','http://127.0.0.1:6082/stats'));
const pause=ms=>new Promise(resolve=>setTimeout(resolve,ms));
const digest=bytes=>createHash('sha256').update(bytes).digest('hex');
const report={startedAt:new Date().toISOString(),result:'failed',samples:[]};
const cookies=new Map();let csrf='',disabled=false,bob,adminLoggedIn=false;
async function request(path,method='GET',form){
  const headers={Accept:'application/json'};
  if(cookies.size)headers.Cookie=[...cookies].map(([k,v])=>k+'='+v).join('; ');
  if(method!=='GET')headers['X-CSRF-Token']=csrf;
  const response=await fetch(origin+path,{method,headers,body:form?new URLSearchParams(form):undefined,
    redirect:'manual',signal:AbortSignal.timeout(15000)});
  for(const cookie of response.headers.getSetCookie()){const first=cookie.split(';')[0],i=first.indexOf('=');cookies.set(first.slice(0,i),first.slice(i+1));}
  const body=await response.text();let data;try{data=JSON.parse(body);}catch{}
  return {status:response.status,data};
}
async function setStatus(status){
  const response=await request('/api/admin/users/'+bob.id+'/status','POST',{status});
  assert.equal(response.status,200,'set disposable Bob '+status+' failed');
}
try{
  const platform=JSON.parse(await readFile('data-e2e/platform-report.json','utf8'));
  assert.equal(platform.run,'mu537vjz','only the specifically authorized disposable run is permitted');
  bob=platform.users.find(u=>u.label==='bob');const alice=platform.users.find(u=>u.label==='alice');
  assert.equal(bob?.id,3);assert.equal(bob.email,'e2e-bob-mu537vjz@example.test');
  const B=platform.instances.find(s=>s.owner===bob.id),A=platform.instances.find(s=>s.owner===alice.id);
  assert.equal(B?.id,'sess_8685794da70b396a');assert(A);
  report.run=platform.run;report.user={id:bob.id,email:bob.email};report.instances={A:A.id,B:B.id};
  const configuration=await readFile('.env','utf8');
  const raw=configuration.match(/^RB_ADMIN_PASSWORD=(.+)$/m)?.[1]?.trim();
  const password=process.env.RB_E2E_ADMIN_PASSWORD||(raw?.replace(/^(['"])(.*)\1$/,'$2'));
  assert(password,'administrator credential unavailable');
  await request('/login');csrf=cookies.get('rb_csrf');
  assert.equal((await request('/api/login','POST',{username:'admin',password})).status,303,'administrator login failed');adminLoggedIn=true;
  const users=await request('/api/admin/users?q='+encodeURIComponent(bob.email));
  assert.equal(users.status,200);assert.equal(users.data.users.find(u=>u.id===bob.id)?.status,'active','Bob must start active');
  const beforeA=inspect(A.id),beforeB=inspect(B.id);
  for(const [instance,owner] of [[beforeA,alice.id],[beforeB,bob.id]]){
    assert.equal(instance.State.Running,true);assert.equal(instance.Config.Labels['remotebrowser.user.id'],String(owner));
  }
  const downloads=beforeA.Mounts.find(m=>m.Destination==='/home/rbuser/Downloads')?.Source;
  assert(downloads,'Alice download mount missing');
  const marker=join(downloads,'持久化验收.txt'),beforeMarker=await readFile(marker);
  assert.equal(beforeMarker.toString(),'RemoteBrowser 中文持久化 '+platform.run,'Alice fixture differs before revocation');
  report.before={AContainerID:beforeA.Id,BContainerID:beforeB.Id,aliceMarkerSHA256:digest(beforeMarker),bobPeerCount:stats(B.id).peerCount};
  assert(report.before.bobPeerCount>=1,'a real connected Bob WebRTC peer is required; no action performed');
  const started=performance.now();report.disabledAt=new Date().toISOString();disabled=true;
  await setStatus('disabled');
  while(true){
    const peerCount=stats(B.id).peerCount,elapsedMs=Math.round(performance.now()-started);
    report.samples.push({elapsedMs,peerCount});
    if(peerCount===0){report.revocationElapsedMs=elapsedMs;break;}
    assert(elapsedMs<10000,'connected WebRTC peer survived the ten-second revocation limit');
    await pause(200);
  }
  assert(report.revocationElapsedMs<=10000,'WebRTC revocation exceeded ten seconds');
  const afterA=inspect(A.id),afterB=inspect(B.id),afterMarker=await readFile(marker);
  assert.equal(afterA.Id,beforeA.Id,'Alice container was replaced');assert.equal(afterB.Id,beforeB.Id,'Bob container was replaced');
  assert.equal(afterA.State.Running,true);assert.equal(afterB.State.Running,true);
  assert.deepEqual(afterMarker,beforeMarker,'Alice data changed');
  report.after={AContainerID:afterA.Id,BContainerID:afterB.Id,AStillRunning:true,BStillRunning:true,aliceMarkerSHA256:digest(afterMarker),bobPeerCount:0};
  report.result='passed';
}catch(error){report.error=error.message;process.exitCode=1;}
finally{
  if(disabled){try{await setStatus('active');report.bobReenabled=true;}catch(error){report.bobReenabled=false;report.result='failed';report.restoreError=error.message;process.exitCode=1;}}
  if(adminLoggedIn){try{await request('/api/logout','POST',{});}catch{}}
  report.completedAt=new Date().toISOString();await mkdir('data-e2e',{recursive:true});
  await writeFile('data-e2e/revocation-report.json',JSON.stringify(report,null,2)+'\n');
  console.log(JSON.stringify({result:report.result,initialPeerCount:report.before?.bobPeerCount,revocationElapsedMs:report.revocationElapsedMs,bobReenabled:report.bobReenabled,error:report.error}));
}
