import React,{useEffect,useSyncExternalStore} from 'react';
import {createRoot} from 'react-dom/client';
import {HashRouter,Route,Routes} from 'react-router-dom';
import {App as AntApp,ConfigProvider,theme as antTheme} from 'antd';
import {getSession,startSessionBridge,subscribeSession} from '@/platform/session';
import {useThemeStore} from '@/stores/use-theme-store';
import {Library} from '@/pages/Library';
import {Editor} from '@/pages/Editor';
import i18n from '@/i18n';
import 'antd/dist/reset.css';
import './styles.css';

function App(){
 const session=useSyncExternalStore(subscribeSession,getSession);const mode=useThemeStore(s=>s.theme);const setTheme=useThemeStore(s=>s.setTheme);
 useEffect(()=>{if(session){setTheme(session.theme);void i18n.changeLanguage(session.locale);}},[session,setTheme]);
 if(!session)return <div className="empty">请从本站工作台打开画布。正在等待登录会话…</div>;
 return <ConfigProvider theme={{algorithm:mode==='dark'?antTheme.darkAlgorithm:antTheme.defaultAlgorithm,token:{colorPrimary:'#12A7E8',colorBgContainer:mode==='dark'?'#202023':'#FFFFFF',borderRadius:12}}}><AntApp><HashRouter key={session.userId}><Routes><Route path="/" element={<Library/>}/><Route path="/:id" element={<Editor/>}/></Routes></HashRouter></AntApp></ConfigProvider>;
}
// 在 React 挂载前建立会话桥，避免父页面发送会话早于 effect 注册监听而丢失。
startSessionBridge();
createRoot(document.getElementById('root')!).render(<React.StrictMode><App/></React.StrictMode>);
