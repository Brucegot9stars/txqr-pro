// Deterministic 60 Hz presentation with RAF jitter and two-refresh image decode.
// Run: node txqr/internal/sender/testdata/throughput.test.cjs
const assert=require('node:assert/strict'),vm=require('node:vm');
const {player,flush}=require('./playback.test.cjs');
async function check(fps,carousel,codes) {
 let tick=0;const decodes=[];
 const p=player(carousel,()=>new Promise(resolve=>decodes.push({at:tick+2,resolve})),codes);
 vm.runInContext(`session.fps=${fps};startPlayback()`,p.context);
 let before=0;const warmed=120,total=480;
 for(tick=0;tick<total;tick++) {
  for(const request of p.requests) if(!request.settled&&!request.signal.aborted) {
   request.settled=true;request.resolve({ok:true,headers:{get:()=> '2048'},blob:async()=>({})});
  }
  for(let i=decodes.length-1;i>=0;i--) if(decodes[i].at<=tick) {decodes[i].resolve();decodes.splice(i,1);}
  await flush();
  const time=tick*1000/60+[-0.2,0.25,-0.1,0.15][tick%4];
  p.tick(time);
  if(tick===warmed-1)before=p.visible.length;
  assert(p.requests.filter(r=>!r.settled&&!r.signal.aborted).length<=3*codes,'request backlog');
 }
 const expected=(total-warmed)/60*fps*codes;
 const displayed=p.visible.length-before;
 assert(displayed>=expected*0.98,`${fps} FPS ${carousel?'carousel':'QR'}: ${displayed} < ${expected}`);
 assert(displayed<=expected+2,'presentation exceeds requested frame rate');
 const rates=vm.runInContext(`playbackRates(${(total-1)*1000/60})`,p.context);
 assert(Math.abs(rates.fps-fps)<1,'HUD frame rate differs from actual per-code presentation');
 assert(Math.abs(rates.bytes-fps*codes*2048)<fps*codes*2048*0.03,'HUD speed differs from displayed payload bytes');
 assert(vm.runInContext('sendMeter.samples.length',p.context)<=150,'speed sample window grew without bound');
 const beforeStall=p.visible.length;
 p.tick(total*1000/60+3000);assert(p.visible.length-beforeStall<=codes,'stall produced a burst');
 p.clear();await flush();assert.equal(p.liveURLs.size,0);
 console.log(`PASS ${fps} FPS ${carousel?'carousel':'QR'} grid=${codes}: ${displayed}/${expected} visible frames`);
}
(async()=>{for(const fps of [30,60])for(const carousel of [false,true])for(const codes of [1,2])await check(fps,carousel,codes);})()
 .catch(error=>{console.error(error);process.exitCode=1;});
