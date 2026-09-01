// 配置页 preload：把 ipcRenderer.handle 的两个调用桥到 window.__api
const { contextBridge, ipcRenderer } = require('electron');
contextBridge.exposeInMainWorld('__api', {
  testConnection: (payload) => ipcRenderer.invoke('ai:testConnection', payload),
  saveAndLaunch: (payload) => ipcRenderer.invoke('ai:saveAndLaunch', payload),
  openExternal: (url) => ipcRenderer.invoke('ext:openExternal', url)
});
