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
function player(carousel=false,autoDecode=true) {
  let nextID=0,now=0;const rafs=new Map(),requests=[],decodes=[],visible=[],liveURLs=new Set();
  function replacement(image){visible.push({image,at:now});}
  class FakeImage {
    constructor(){this.style={cssText:''};}
    decode(){return autoDecode?Promise.resolve():new Promise(resolve=>decodes.push(resolve));}
    removeAttribute(){}
    replaceWith(image){replacement(image);}
  }
  const context=vm.createContext({
    performance:{now:()=>now},pauseBtn:{},statsEl:{},AbortController,Image:FakeImage,
    URL:{createObjectURL(){const url='blob:'+ ++nextID;liveURLs.add(url);return url;},revokeObjectURL(url){liveURLs.delete(url);}},
    requestAnimationFrame(fn){const id=++nextID;rafs.set(id,fn);return id;},cancelAnimationFrame:id=>rafs.delete(id),
    fetch(url,options){return new Promise((resolve,reject)=>{
      requests.push({url,signal:options.signal,resolve,reject});
      options.signal.addEventListener('abort',()=>reject(Object.assign(new Error('aborted'),{name:'AbortError'})));
    });},
    tiles:[new FakeImage(),new FakeImage()],
  });
  vm.runInContext(`
    let playbackRAF=null,playbackGeneration=0,playbackCells=[],cellCursor=0,presentedFrames=0;
    let running=true,paused=false,seqCursor=0,startTime=0;
    let session={carousel:${carousel},codes:2,count:16,fps:30,dims:{cols:2,rows:1}};
    function sizeNote(){return ''}
    ${['releaseFrame','releaseTiles','clearPlayback','showFrame','startDecimenLoop','startPlayback','prefetchFrame','playbackTick','togglePause'].map(extract).join('\n')}
    startPlayback();
  `,context);
  return {context,rafs,requests,decodes,visible,liveURLs,
    tick(time){now=time;const callbacks=[...rafs.values()];rafs.clear();assert(callbacks.length<=1,'duplicate RAF');for(const f of callbacks)f(time);},
    ready(index){requests[index].resolve({ok:true,blob:async()=>({})});},
    clear(){vm.runInContext('clearPlayback();releaseTiles()',context);},
  };
}
async function tests() {
  for(const carousel of [false,true]) {
    const p=player(carousel);
    for(let i=0;i<100;i++) {
      vm.runInContext('togglePause()',p.context);assert.equal(p.rafs.size,0);
      vm.runInContext('togglePause()',p.context);assert.equal(p.rafs.size,1);
      assert.equal(p.requests.filter(r=>!r.signal.aborted).length,2,'unbounded fetches');
    }
    p.clear();await flush();assert.equal(p.liveURLs.size,0);
  }
  {
    const p=player(false,false);
    for(let t=0;t<100;t++)p.tick(t);
    assert.equal(p.requests.length,2,'slow fetch accumulated requests');assert.equal(p.visible.length,0);
    p.ready(0);await flush();p.tick(101);assert.equal(p.visible.length,0,'undecoded image presented');
    p.decodes[0]();await flush();p.tick(102);assert.equal(p.visible.length,1);
    p.ready(2);await flush();p.decodes[1]();await flush();
    p.tick(103);assert.equal(p.visible.length,1,'frame exposure cut short');
    p.tick(136);assert.equal(p.visible.length,2);p.clear();await flush();assert.equal(p.liveURLs.size,0);
  }
  {
    const p=player(true);p.ready(0);p.ready(1);await flush();p.tick(100);
    assert.equal(p.visible.length,1,'carousel flipped multiple cells in one refresh');
    p.tick(117);assert.equal(p.visible.length,2,'carousel cell starved');p.clear();await flush();
  }
  {
    const p=player(false,false);p.ready(0);await flush();assert.equal(p.liveURLs.size,1);
    p.clear();p.decodes[0]();await flush();p.tick(100);assert.equal(p.visible.length,0,'stale decode presented');assert.equal(p.liveURLs.size,0);
  }
  {
    const p=player();p.requests[0].resolve({ok:false,status:500});await flush();
    p.tick(100);assert.equal(p.requests.length,2,'error caused a retry storm');
    p.tick(251);assert.equal(p.requests.length,3);assert.equal(p.visible.length,0);p.clear();await flush();
  }
  const pending=new Map();let builds=0;
  const context=vm.createContext({pending,performance:{now:()=>1000},pauseBtn:{},overlay:{classList:{add(){}}},
    buildTiles(){builds++;},requestAnimationFrame(){},applyFrameSize(){},showFrame(){},startDecimenLoop(){}});
  vm.runInContext(`
    let encodeGeneration=0,running=false,session=null,base=0,shownBytes=0,presentedFrames=0,seqCursor=0,startTime=0,paused=false;
    function clearPlayback(){} async function cancelSession(){session=null}
    function encodeFile(generation){return new Promise(resolve=>pending.set(generation,resolve))}
    ${extract('start')}
  `,context);
  const first=vm.runInContext('start()',context),second=vm.runInContext('start()',context);
  pending.get(2)({id:'new',codes:1,dims:{},carousel:false});await second;
  pending.get(1)({id:'stale',codes:1,dims:{},carousel:false});await first;
  assert.equal(vm.runInContext('session.id',context),'new');assert.equal(builds,1);
  console.log('PASS: bounded loading, decode-before-present, exposure, fair carousel, cancellation, retries, pause/resume, stale responses');
}
tests().catch(error=>{console.error(error);process.exitCode=1;});
