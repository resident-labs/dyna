package visual

const contrastScript = `(() => {` + visibleElementScript + `
 // Canvas converts all browser-supported CSS colors to sRGB, including OKLCH.
 const canvas=document.createElement('canvas'); canvas.width=canvas.height=1;
 const context=canvas.getContext('2d',{willReadFrequently:true}), colors=new Map();
 const rgba = s => {
 if(!colors.has(s)){
 context.clearRect(0,0,1,1); context.fillStyle=s; context.fillRect(0,0,1,1);
 const pixel=[...context.getImageData(0,0,1,1).data];pixel[3]/=255;colors.set(s,pixel);
 }
 return colors.get(s);
 };
 const blend = (fg, bg) => fg.slice(0,3).map((c,i) => c * (fg[3] ?? 1) + bg[i] * (1-(fg[3] ?? 1)));
 const lum = rgb => rgb.map(v => {v/=255; return v<=.04045?v/12.92:((v+.055)/1.055)**2.4}).reduce((s,v,i)=>s+v*[.2126,.7152,.0722][i],0);
 let count=0, sample='';
 for (const el of document.querySelectorAll('body, body *')) {
 const text=[...el.childNodes].filter(n=>n.nodeType===Node.TEXT_NODE).map(n=>n.textContent).join('').trim();
 if (!text) continue;
 const style=getComputedStyle(el);
 if (!visibleElement(el)) continue;
 const layers=[]; let skip=false;
 for(let p=el;p;p=p.parentElement){
 const s=getComputedStyle(p);
 if(s.backgroundImage!=='none'){skip=true;break}
 const color=rgba(s.backgroundColor);layers.push(color);
 if((color[3]??1)===1)break;
 }
 if(skip)continue;
 let bg=[255,255,255];
 for(const layer of layers.reverse())bg=blend(layer,bg);
 const fg=blend(rgba(style.color),bg), a=lum(fg), b=lum(bg);
 const ratio=(Math.max(a,b)+.05)/(Math.min(a,b)+.05);
 const large=parseFloat(style.fontSize)>=24 || (Number(style.fontWeight)>=700 && parseFloat(style.fontSize)>=18.66);
 if(ratio<(large?3:4.5)){count++;if(!sample)sample=JSON.stringify(text.slice(0,60))+' '+ratio.toFixed(1)+':1'}
 }
 return {count,sample};
})()`
