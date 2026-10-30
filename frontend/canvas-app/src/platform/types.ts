import type {CanvasConnection, CanvasNodeData, ViewportTransform} from '@/types/canvas';
export type CanvasDocument = {nodes: CanvasNodeData[]; connections: CanvasConnection[]; viewport: ViewportTransform; backgroundMode?: 'lines'|'dots'|'blank'};
export type CanvasRecord = {id:string; title:string; document:CanvasDocument; revision:number; created_at:string; updated_at:string};
export type Page<T> = {items:T[]; total:number; page:number; page_size:number};
export type Asset = {id:string; canvas_id?:string; media_type:'image'|'video'|'audio'; mime_type:string; byte_size:number; expires_at:string; created_at:string};
export type VideoCapabilities = {
  durations?:number[]; resolutions?:string[]; aspect_ratios?:string[]; max_reference_images?:number;
  // 参考视频数量已由服务端取管理员配置与协议上限的较小值；省略或 0 表示该模型不接受参考视频。
  max_reference_videos?:number;
  // 可选的本站单图提交限制；省略时不在浏览器猜测限制，仍由服务端校验。
  max_reference_image_bytes?:number;
  allow_custom_resolution?:boolean; allow_custom_aspect_ratio?:boolean;
  default_duration?:number; default_resolution?:string; default_aspect_ratio?:string;
};
export type GenerationKind = 'image'|'video'|'audio'|'text';
export type TextCapabilities = {max_input_chars:number;max_output_tokens:number;default_max_output_tokens:number;max_reference_images:number};
export type AudioCapabilities = {voices:string[];formats:string[];speed_min:number;speed_max:number;default_voice:string;default_format:string;default_speed:number;max_input_chars:number};
export type Model = {group_id:number; group_name:string; model:string; display_name?:string; usage_notes?:string; media_type?:GenerationKind; operations:string[]; image_sizes?:string[]; sizes?:string[]; aspect_ratios:string[]; qualities:string[]; background_options:string[]; thinking_levels?:string[]; output_count?:number; max_reference_images:number; video_capabilities?:VideoCapabilities; audio_capabilities?:AudioCapabilities; text_capabilities?:TextCapabilities; video_endpoint?:string; video_endpoints?:string[]; video_resolutions?:string[]; video_durations?:number[]; video_aspect_ratios?:string[]; price_512?:number|null; price_1k?:number|null; price_2k?:number|null; price_4k?:number|null; [key:string]:unknown};
export type Execution = {id:string; canvas_id:string; node_id:string; kind:GenerationKind; status:'queued'|'running'|'succeeded'|'failed'|'result_lost'; task_id:string; assets:Asset[]; output_text?:string; error_code?:string; error_message?:string; batch_id?:string; batch_index?:number; batch_size?:number; batch_output_count?:number; created_at:string; updated_at:string};
export type ExecutionBatch = {batch_id:string; executions:Execution[]};
// 提交意图先随画布保存；响应丢失后只能用原键和原参数核对，不自动再生成。
export type GenerationRequest = {key:string; kind:GenerationKind; params:ExecuteParams; count:number};
export type ExecuteParams = {group_id:number; model:string; operation:string; prompt:string; image_size?:string; aspect_ratio?:string; quality?:string; background?:string; thinking_level?:string; voice?:string; audio_format?:string; speed?:number; max_output_tokens?:number; endpoint?:string; duration?:number; resolution?:string; references?:{asset_id:string; role?:string}[]; mask_asset_id?:string};
export const modelId = (m: Model) => `${m.group_id}::${m.model}`;
export const mediaType = (m: Model) => m.media_type || 'image';
export const blankDocument = (): CanvasDocument => ({nodes:[],connections:[],viewport:{x:0,y:0,k:1},backgroundMode:'lines'});
