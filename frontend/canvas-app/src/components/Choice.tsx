import {Select,SelectTrigger,SelectContent,SelectItem,SelectValue} from './ui/select';
// 复用原画布自研 Radix 选择框，不使用原生 select。
export function Choice({value,options,onChange,label,compact=false}:{value:string;options:{value:string;label:string}[];onChange:(value:string)=>void;label:string;compact?:boolean}){
  return <div className={`field ${compact?'choice-compact':''}`}>{!compact&&label&&<span>{label}</span>}<Select value={value} onValueChange={onChange}><SelectTrigger aria-label={label||'选择模型'} title={options.find(o=>o.value===value)?.label}><SelectValue placeholder="选择模型"/></SelectTrigger><SelectContent data-canvas-no-zoom onKeyDown={event=>event.stopPropagation()} className="canvas-choice-content" position="popper" align="start" side="bottom" sideOffset={6} collisionPadding={12}>{options.filter(o=>o.value).map(o=><SelectItem key={o.value} value={o.value} title={o.label}>{o.label}</SelectItem>)}</SelectContent></Select></div>;
}
