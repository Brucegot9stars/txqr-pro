// node txqr/internal/sender/testdata/playback.test.cjs -- no dependencies/browser required.
const fs=require('node:fs'),vm=require('node:vm'),assert=require('node:assert/strict'),path=require('node:path');
const page=fs.readFileSync(path.join(__dirname,'..','index.html'),'utf8');
function extract(name) {
  const start=page.indexOf('function '+name+'(');assert(start>=0,'missing '+name);
  let end=page.indexOf('{',start)+1,depth=1;
  for(;depth;end++){if(page[end]==='{')depth++;if(page[end]==='}')depth--;}
  return (page.slice(start-6,start)==='async '?'async ':'')+page.slice(start,end);
}
const flush=async()=>{for(let i=0;i<12;i++)await Promise.resolve();};
function player(carousel=false,autoDecode=true,codes=2) {
  let nextID=0,now=0;const rafs=new Map(),requests=[],decodes=[],visible=[],liveURLs=new Set();
  function replacement(image){visible.push({image,at:now});}
  class FakeImage {
    constructor(){this.style={cssText:''};}
    decode(){return typeof autoDecode==='function'?autoDecode():(autoDecode?Promise.resolve():new Promise(resolve=>decodes.push(resolve)));}
    removeAttribute(){}
    replaceWith(image){replacement(image);}
  }
  const context=vm.createContext({
    performance:{now:()=>now},pauseBtn:{},statsEl:{},sendRateEl:{},displayRateEl:{},sentBytesEl:{},playbackInfoEl:{},playbackNoticeEl:{},AbortController,Image:FakeImage,
    URL:{createObjectURL(){const url='blob:'+ ++nextID;liveURLs.add(url);return url;},revokeObjectURL(url){liveURLs.delete(url);}},
    requestAnimationFrame(fn){const id=++nextID;rafs.set(id,fn);return id;},cancelAnimationFrame:id=>rafs.delete(id),
    fetch(url,options){return new Promise((resolve,reject)=>{
      requests.push({url,signal:options.signal,resolve,reject});
      options.signal.addEventListener('abort',()=>reject(Object.assign(new Error('aborted'),{name:'AbortError'})));
    });},
    tiles:Array.from({length:codes},()=>new FakeImage()),
  });
  vm.runInContext(`
    const PREFETCH_DEPTH=3,PRESENTATION_TOLERANCE_MS=0.75;
    let playbackRAF=null,playbackGeneration=0,playbackCells=[],playbackEpoch=null,presentedFrames=0;
    let running=true,paused=false,seqCursor=0,startTime=0;
    let sendMeter,playbackError='';
    let session={carousel:${carousel},codes:${codes},count:16,fps:30,dims:{cols:${codes},rows:1}};
    function sizeNote(){return ''}
    ${['bytesFmt','resetSendStats','resetSendWindow','recordPresentedFrame','playbackRates','updatePlaybackStats','releaseFrame','releaseTiles','clearPlayback','showFrame','startDecimenLoop','startPlayback','fillPrefetch','prefetchFrame','playbackTick','togglePause'].map(extract).join('\n')}
    resetSendStats(0);
    startPlayback();
  `,context);
  return {context,rafs,requests,decodes,visible,liveURLs,
    tick(time){now=time;const callbacks=[...rafs.values()];rafs.clear();assert(callbacks.length<=1,'duplicate RAF');for(const f of callbacks)f(time);},
    ready(index,bytes=2048){requests[index].resolve({ok:true,headers:{get:()=>bytes===null?null:String(bytes)},blob:async()=>({})});},
    clear(){vm.runInContext('clearPlayback();releaseTiles()',context);},
  };
}
async function tests() {
  for(const carousel of [false,true]) {
    const p=player(carousel);
    for(let i=0;i<100;i++) {
      vm.runInContext('togglePause()',p.context);assert.equal(p.rafs.size,0);
      vm.runInContext('togglePause()',p.context);assert.equal(p.rafs.size,1);
      assert.equal(p.requests.filter(r=>!r.signal.aborted).length,6,'unbounded fetches');
    }
    p.clear();await flush();assert.equal(p.liveURLs.size,0);
  }
  {
    const p=player(false,false);
    for(let t=0;t<100;t++)p.tick(t);
    assert.equal(p.requests.length,6,'slow fetch accumulated requests');assert.equal(p.visible.length,0);
    p.ready(0);await flush();p.tick(101);assert.equal(p.visible.length,0,'undecoded image presented');
    p.decodes[0]();await flush();p.tick(102);assert.equal(p.visible.length,1);
    p.ready(2);await flush();p.decodes[1]();await flush();
    p.tick(103);assert.equal(p.visible.length,1,'frame exposure cut short');
    p.tick(136);assert.equal(p.visible.length,2);p.clear();await flush();assert.equal(p.liveURLs.size,0);
  }
  {
    const p=player(true);p.ready(0);p.ready(1);await flush();p.tick(100);
    assert.equal(p.visible.length,1,'carousel phase start changed');
    p.tick(117);assert.equal(p.visible.length,2,'carousel cell starved');p.clear();await flush();
  }
  {
    const p=player(false,false);p.ready(0);await flush();assert.equal(p.liveURLs.size,1);
    p.clear();p.decodes[0]();await flush();p.tick(100);assert.equal(p.visible.length,0,'stale decode presented');assert.equal(p.liveURLs.size,0);
  }
  {
    const p=player();p.requests[0].resolve({ok:false,status:500});await flush();
    p.tick(100);assert.equal(p.requests.length,6,'error caused a retry storm');
    p.tick(251);assert.equal(p.requests.length,7);assert.equal(p.visible.length,0);p.clear();await flush();
  }
  {
    const p=player(false,true,1);
    p.ready(0,0);p.ready(1,512);p.ready(2,1000);await flush();
    assert.equal(vm.runInContext('sendMeter.totalBytes',p.context),0,'prefetch counted as sending');
    p.tick(100);assert.equal(vm.runInContext('sendMeter.totalBytes',p.context),0,'filename counted as data');
    p.tick(134);assert.equal(vm.runInContext('sendMeter.totalBytes',p.context),512);
    p.tick(168);assert.equal(vm.runInContext('sendMeter.totalBytes',p.context),1512,'used split instead of actual payload');
    p.tick(250);assert.equal(vm.runInContext('playbackRates(250).bytes',p.context),6048);
    p.tick(3000);assert.equal(vm.runInContext('playbackRates(3000).bytes',p.context),0,'stall retained a stale speed');
    vm.runInContext('togglePause()',p.context);assert.equal(p.context.sendRateEl.textContent,'0 KB/s');
    vm.runInContext('togglePause()',p.context);assert.equal(vm.runInContext('sendMeter.totalBytes',p.context),1512,'resume lost sent bytes');
    p.clear();await flush();
  }
  {
    const p=player(false,true,1);p.ready(0,null);await flush();p.tick(100);
    assert.equal(vm.runInContext('playbackRates(250).bytes',p.context),null,'missing payload header invented a rate');
    p.clear();await flush();
  }
  const pending=new Map();let builds=0;
  const context=vm.createContext({pending,performance:{now:()=>1000},pauseBtn:{},overlay:{classList:{add(){}}},
    buildTiles(){builds++;},requestAnimationFrame(){},applyFrameSize(){},showFrame(){},startDecimenLoop(){}});
  vm.runInContext(`
    let encodeGeneration=0,running=false,session=null,base=0,shownBytes=0,presentedFrames=0,seqCursor=0,startTime=0,paused=false;
    function clearPlayback(){} function finishPreparation(){} function resetSendStats(){} async function cancelSession(){session=null}
    function encodeFile(generation){return new Promise(resolve=>pending.set(generation,resolve))}
    ${extract('start')}
  `,context);
  const first=vm.runInContext('start()',context),second=vm.runInContext('start()',context);
  pending.get(2)({id:'new',codes:1,dims:{},carousel:false});await second;
  pending.get(1)({id:'stale',codes:1,dims:{},carousel:false});await first;
  assert.equal(vm.runInContext('session.id',context),'new');assert.equal(builds,1);
  console.log('PASS: bounded loading, decode-before-present, exposure, fair carousel, cancellation, retries, pause/resume, stale responses');
}
module.exports={player,flush};
if(require.main===module)tests().catch(error=>{console.error(error);process.exitCode=1;});
