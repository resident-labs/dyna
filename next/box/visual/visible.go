package visual

// Both render and contrast checks use the same visibility rules.
const visibleElementScript = `
 const visibleElement = el => {
 if(getComputedStyle(el).visibility!=='visible')return false;
 for(let p=el;p;p=p.parentElement){
 const s=getComputedStyle(p);
 if(s.display==='none'||Number(s.opacity)===0)return false;
 }
 const nonempty=r=>r.width>0&&r.height>0;
 if([...el.getClientRects()].some(nonempty))return true;
 // display:contents has no element box, but its text still has rendered ranges.
 const range=document.createRange();range.selectNodeContents(el);
 return [...range.getClientRects()].some(nonempty);
 };
`

const visibleContentScript = `(() => {` + visibleElementScript + `
 return [...document.querySelectorAll('body, body *')].some(el => {
 if(!visibleElement(el))return false;
 if(el.matches('img,svg,canvas,video'))return true;
 return [...el.childNodes].some(n=>n.nodeType===Node.TEXT_NODE&&n.textContent.trim());
 });
})()`

// Fetch interception does not cover WebSockets. A second CSP blocks non-HTTPS
// connections, while policy violations still report the attempted host.
const policyBinding = "__dynaVisualPolicyBlocked"
const policyScript = `addEventListener('securitypolicyviolation', e => {
 if(e.blockedURI) __dynaVisualPolicyBlocked(e.blockedURI);
})`
