import { create } from 'zustand';
import type { CanvasColorTheme } from '@/lib/canvas-theme';

// 主题仅存在内存，避免与主站或其他用户的持久配置混用。
export const useThemeStore = create<{theme: CanvasColorTheme; setTheme: (theme: CanvasColorTheme) => void}>((set) => ({
  theme: 'dark', setTheme: (theme) => { document.documentElement.classList.toggle('dark', theme === 'dark'); set({theme}); },
}));
