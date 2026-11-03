import fs from 'node:fs';
process.chdir(new URL('.', import.meta.url).pathname);
// Commitarium mark: a hexagon C (lead + reviewer) holding a blue hexagon (the commit).
// Built on a triangle grid: x in triangle sides, y in triangle heights.
const h=Math.sqrt(3)/2*100;
const p=a=>a.map(([x,y])=>`${+(x*100).toFixed(2)},${+(y*h).toFixed(2)}`).join(' ');
const C=p([[1.5,1],[1,2],[-1,2],[-2,0],[-1,-2],[1,-2],[1.5,-1],[0.5,-1],[-0.5,-1],[-1,0],[-0.5,1],[0.5,1]]);
const r=0.68,cx=1.0; // even margin to the C and to the missing corner
const B=p([[cx+r,0],[cx+r/2,r],[cx-r/2,r],[cx-r,0],[cx-r/2,-r],[cx+r/2,-r]]);
const pal={light:{c:'#171717',b:'#0070f3'},dark:{c:'#ededed',b:'#3b8eff'}};
const css=t=>`.c{fill:${pal[t].c}}.b{fill:${pal[t].b}}`;
const shapes=`<polygon class="c" points="${C}"/><polygon class="b" points="${B}"/>`;
const VB='-215 -215 430 430';
const svg=(style,body,vb=VB)=>`<svg xmlns="http://www.w3.org/2000/svg" viewBox="${vb}"><title>Commitarium</title><style>${style}</style>${body}</svg>\n`;
fs.writeFileSync('commitarium-mark.svg',svg(css('light'),shapes));
fs.writeFileSync('commitarium-mark-dark.svg',svg(css('dark'),shapes));
fs.writeFileSync('favicon.svg',svg(css('light')+`@media (prefers-color-scheme:dark){${css('dark')}}`,shapes));
const sc=1024*0.62/400;
const icon=t=>{const bg=t==='dark'?'#0a0a0a':'#ffffff',ed=t==='dark'?'#262626':'#e5e5e5';
 return svg(css(t),`<rect x="100" y="100" width="824" height="824" rx="185" fill="${bg}" stroke="${ed}" stroke-width="2"/><g transform="translate(512 512) scale(${sc.toFixed(4)})">${shapes}</g>`,'0 0 1024 1024')};
fs.writeFileSync('app-icon.svg',icon('dark'));
