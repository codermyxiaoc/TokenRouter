import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';
import zh from './locales/zh-CN';
import en from './locales/en-US';

// 复用原编辑器中文操作说明，语言由站点宿主控制。
void i18n.use(initReactI18next).init({resources: {'zh-CN': {translation: zh}, en: {translation: en}}, lng: 'zh-CN', fallbackLng: 'zh-CN', interpolation: {escapeValue: false}});
export default i18n;
