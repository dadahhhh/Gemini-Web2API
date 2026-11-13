// Exercise account management requests without a browser, Google cookies or network.
import fs from 'node:fs';
import vm from 'node:vm';
import assert from 'node:assert/strict';
const page=fs.readFileSync(new URL('../panel.html',import.meta.url),'utf8');
class Element {
 constructor(){this.value='';this.files=[];this.children=[];this.checked=true;this.textContent=''}
 append(...elements){this.children.push(...elements)}
 replaceChildren(...elements){this.children=elements}
 scrollIntoView(){}
 focus(){}
}
const elements=new Map(),requests=[];
const element=id=>{if(!elements.has(id))elements.set(id,new Element());return elements.get(id)};
const context=vm.createContext({TextEncoder,Date,console,setInterval(){},confirm(){return true},document:{hidden:false,getElementById:element,createElement(){return new Element()}},fetch:async(url,options)=>{
 const body=options.body?JSON.parse(options.body):null;requests.push({url,body});
 let response={version:'0.2.0',routing_strategy:'round-robin',per_account_concurrency:1,total:0,available:0,cooling_down:0,disabled:0,accounts:[]};
 if(url.endsWith('/import'))response={id:body.id||'auto-first',message:'saved'};
 if(url.endsWith('/import-batch'))response={saved:1,failed:1,results:[{index:0,saved:true,id:'batch-first'},{index:1,saved:false,error:{message:'duplicate session'}}]};
 return {ok:true,status:200,async json(){return response}};
}});
vm.runInContext(page.match(/<script>([\s\S]*?)<\/script>/)[1],context);
element('key').value='synthetic-management-key';element('material').value='synthetic-cookie';
await element('import').onclick();
assert.equal(requests.find(x=>x.url.endsWith('/import')).body.mode,'create');
assert.equal(element('account').value,'','new imports must clear the prior account ID');
element('material').value='second-cookie';await element('import').onclick();
assert.equal(requests.filter(x=>x.url.endsWith('/import'))[1].body.id,'');
vm.runInContext('edit({id:"existing-account"})',context);
element('material').value='replacement';await element('import').onclick();
const update=requests.filter(x=>x.url.endsWith('/import'))[2].body;
assert.equal(update.id,'existing-account');assert.equal(update.mode,'update');
element('files');element('file').files=[{name:'first.json',size:20,text:async()=>'{"synthetic":1}'},{name:'second.json',size:20,text:async()=>'{"synthetic":2}'}];
await element('import').onclick();
const batch=requests.find(x=>x.url.endsWith('/import-batch'));assert.equal(batch.body.accounts.length,2);
assert.match(element('result').textContent,/已保存 1 个，失败 1 个/);
assert.match(element('result').textContent,/second.json：duplicate session/);
console.log('PASS: create does not reuse IDs, explicit credential update, batch imports and partial failures');
