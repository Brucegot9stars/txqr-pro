// CHROME=/path/to/chrome node txqr/internal/sender/testdata/browser.test.cjs
// Uses Chrome DevTools pipes, no package dependencies, listener or product build.
const fs=require('node:fs'),os=require('node:os'),path=require('node:path'),{spawn}=require('node:child_process'),assert=require('node:assert/strict');
const chrome=process.env.CHROME;if(!chrome)throw new Error('Set CHROME to the installed Chrome executable');
const profile=fs.mkdtempSync(path.join(os.tmpdir(),'qrt-browser-'));
const child=spawn(chrome,['--headless=new','--no-sandbox','--disable-gpu','--disable-dev-shm-usage','--disable-background-networking','--disable-component-update','--no-first-run','--remote-debugging-pipe','--user-data-dir='+profile,'about:blank'],{stdio:['ignore','ignore','pipe','pipe','pipe']});
let input=Buffer.alloc(0),seq=0,stderr='';const callbacks=new Map();
child.stderr.on('data',data=>{stderr=(stderr+data).slice(-4096);});
child.stdio[4].on('data',data=>{
  input=Buffer.concat([input,data]);let end;
  while((end=input.indexOf(0))>=0){const msg=JSON.parse(input.subarray(0,end).toString());input=input.subarray(end+1);
    const cb=callbacks.get(msg.id);if(cb){callbacks.delete(msg.id);msg.error?cb.reject(new Error(JSON.stringify(msg.error))):cb.resolve(msg.result);}}
});
for(const pipe of [child.stdio[3],child.stdio[4]])pipe.on('error',error=>{for(const cb of callbacks.values())cb.reject(new Error(error.message+' '+stderr));callbacks.clear();});
child.on('exit',()=>{for(const cb of callbacks.values())cb.reject(new Error('Chrome exited: '+stderr));callbacks.clear();});
function cdp(method,params={},sessionId){const id=++seq;return new Promise((resolve,reject)=>{callbacks.set(id,{resolve,reject});child.stdio[3].write(JSON.stringify({id,method,params,...(sessionId?{sessionId}:{})})+'\0');});}
const sleep=ms=>new Promise(resolve=>setTimeout(resolve,ms));
(async()=>{
 const {targetId}=await cdp('Target.createTarget',{url:'about:blank'});
 const {sessionId}=await cdp('Target.attachToTarget',{targetId,flatten:true});
 const evaluate=async expression=>{const r=await cdp('Runtime.evaluate',{expression,returnByValue:true,awaitPromise:true},sessionId);if(r.exceptionDetails)throw new Error(JSON.stringify(r.exceptionDetails));return r.result.value;};
 await cdp('Page.enable',{},sessionId);
 const {frameTree}=await cdp('Page.getFrameTree',{},sessionId);
 const png=fs.readFileSync(path.join(__dirname,'../../../../txqr-reader/android/app/src/test/resources/regression/color.png')).toString('base64');
 const setup=`<script>
 window.fixtureActive=0;window.fixturePeak=0;window.fixtureFail=false;window.fixturePaints=[];window.fixtureURLs=new Set();
 const originalCreate=URL.createObjectURL.bind(URL),originalRevoke=URL.revokeObjectURL.bind(URL);
 URL.createObjectURL=blob=>{const url=originalCreate(blob);fixtureURLs.add(url);return url};
 URL.revokeObjectURL=url=>{fixtureURLs.delete(url);originalRevoke(url)};
 window.fetch=async(url,options={})=>{
   if(!String(url).startsWith('/frame/'))return new Response('{}',{status:200});
   fixtureActive++;fixturePeak=Math.max(fixturePeak,fixtureActive);
   try {
     await new Promise((resolve,reject)=>{
       const timer=setTimeout(resolve,55);
       options.signal?.addEventListener('abort',()=>{clearTimeout(timer);reject(new DOMException('aborted','AbortError'))},{once:true});
     });
     if(fixtureFail)return new Response('',{status:500});
     const bytes=Uint8Array.from(atob('${png}'),c=>c.charCodeAt(0));
     return new Response(new Blob([bytes],{type:'image/png'}));
   } finally {fixtureActive--;}
 };
 </script>`;
 const config={version:'2.0.4',maxUploadBytes:64*1024*1024,maxSourceBlocks:8192,maxChunkBytes:2933,qrFrameBytes:2331,colorFrameBytes:1587,colorSplitBytes:1426,decimenBlockBytes:2933,workerMemoryBytes:512*1024*1024,pngCacheBytes:128*1024*1024,sessionCacheBytes:256*1024*1024,maxSessions:4,encodeTimeoutSeconds:300};
 let html=fs.readFileSync(path.join(__dirname,'..','index.html'),'utf8').replace('/*APP_CONFIG*/ {}',JSON.stringify(config)).replace('<head>','<head>'+setup);
 await cdp('Page.setDocumentContent',{frameId:frameTree.frame.id,html},sessionId);
 assert((await evaluate('document.getElementById("version").textContent')).includes('2.0.4'),'missing displayed version');
 assert(await evaluate('prepareInput.type === "checkbox" && prepareInput.getAttribute("role") === "switch" && !prepareInput.checked'),'pre-generation switch should default off');
 await evaluate('prepareInput.click()');
 assert(await evaluate('prepareInput.checked'),'pre-generation switch did not turn on');
 await evaluate('prepareInput.click()');
 assert(await evaluate('!prepareInput.checked'),'pre-generation switch did not turn off');
 await evaluate(`fileInput.files=(()=>{const transfer=new DataTransfer();transfer.items.add(new File([new Uint8Array(25585664)],'large.bin'));return transfer.files})();fileInput.dispatchEvent(new Event('change'))`);
 await sleep(50);
 assert(await evaluate('startBtn.disabled && document.getElementById("selection-error").textContent.includes("无法仅靠增大分片")'),'24.4 MiB was not explained before encoding');
 await evaluate('fileInput.value="";fileInput.dispatchEvent(new Event("change"))');await sleep(50);
 await evaluate(`
   new MutationObserver(records=>{for(const r of records)for(const n of r.addedNodes)if(n.tagName==='IMG'&&n.src.startsWith('blob:'))fixturePaints.push({time:performance.now(),width:n.naturalWidth,ready:n.complete})}).observe(gridEl,{childList:true});
   session={id:'fixture',count:16,codes:2,carousel:true,fps:60,dims:{cols:1,rows:2},modules:96,ppm:4,k:8};
   buildTiles(2,session.dims);running=true;paused=false;startTime=performance.now();overlay.classList.add('active');applyFrameSize();startPlayback();
 `);
 await sleep(650);
 const state=await evaluate(`({presented:presentedFrames,peak:fixturePeak,images:fixturePaints,visible:tiles.every(t=>t.complete&&t.naturalWidth===384)})`);
 assert(state.presented>=4,'slow loading never presented frames');assert(state.peak<=6,'unbounded prefetch');assert(state.visible);assert(state.images.every(i=>i.ready&&i.width===384),'undecoded image inserted');
 const before=await evaluate('togglePause();presentedFrames');await sleep(180);assert.equal(await evaluate('presentedFrames'),before,'paused presentation continued');
 assert((await evaluate('fixtureURLs.size'))<=2,'pause retained next-image URLs');
 await evaluate('togglePause()');await sleep(400);assert((await evaluate('presentedFrames'))>before);
 await evaluate('fixtureFail=true');await sleep(400);assert(await evaluate('tiles.every(t=>t.complete&&t.naturalWidth===384)'),'failure blanked current frame');
 await evaluate('cancel()');await sleep(80);assert.equal(await evaluate('fixtureURLs.size'),0,'URLs leaked after cancel');assert.equal(await evaluate('fixtureActive'),0);
 console.log('PASS: real Chrome decoding/presentation, bounded slow loads, parallel carousel, pause/resume, failures, URL cleanup');
})().catch(error=>{console.error(error);process.exitCode=1;}).finally(async()=>{
 await Promise.race([cdp('Browser.close').catch(()=>{}),sleep(1000)]);child.kill();
 await new Promise(resolve=>{if(child.exitCode!==null)resolve();else {child.once('exit',resolve);setTimeout(()=>{child.kill('SIGKILL');resolve();},1000).unref();}});
 fs.rmSync(profile,{recursive:true,force:true});
});
