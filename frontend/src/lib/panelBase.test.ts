import { describe, expect, it } from 'vitest';
import { panelBaseForBrowser } from './panelBase';

describe('panelBaseForBrowser（0.6.349 自定义面板端口）', () => {
  it('回环地址换成本页面主机名，端口保留', () => {
    expect(panelBaseForBrowser('http://127.0.0.1:8080', '192.0.2.15')).toBe('http://192.0.2.15:8080');
  });

  it('https 自定义端口同样保留协议', () => {
    expect(panelBaseForBrowser('https://127.0.0.1:8443', 'nas.example.com')).toBe('https://nas.example.com:8443');
  });

  it('页面本身是回环时不替换', () => {
    expect(panelBaseForBrowser('http://127.0.0.1:8080', '127.0.0.1')).toBe('http://127.0.0.1:8080');
    expect(panelBaseForBrowser('http://127.0.0.1:8080', 'localhost')).toBe('http://127.0.0.1:8080');
  });

  it('非回环地址原样返回', () => {
    expect(panelBaseForBrowser('http://192.0.2.22:5666', '192.0.2.15')).toBe('http://192.0.2.22:5666');
  });

  it('非法输入不抛错', () => {
    expect(panelBaseForBrowser('not a url', '192.0.2.15')).toBe('not a url');
  });
});
