// ESLint 9 flat config（ESLint 9 已废 .eslintrc；此前仓库无任何配置，
// 导致 `npm run lint` 直接 exit 2 —— lint 门等于不存在。2026-10-30 补齐。）
import js from '@eslint/js'
import globals from 'globals'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import tseslint from 'typescript-eslint'

export default tseslint.config(
  { ignores: ['dist', 'node_modules'] },
  {
    files: ['**/*.{ts,tsx}'],
    extends: [js.configs.recommended, ...tseslint.configs.recommended],
    languageOptions: {
      ecmaVersion: 2022,
      globals: { ...globals.browser, ...globals.es2021 },
    },
    plugins: {
      'react-hooks': reactHooks,
      'react-refresh': reactRefresh,
    },
    rules: {
      ...reactHooks.configs.recommended.rules,
      'react-refresh/only-export-components': ['warn', { allowConstantExport: true }],
      // 既有代码大量使用 any/非空断言：先降为警告，逐步收口（不阻塞门）
      '@typescript-eslint/no-explicit-any': 'warn',
      '@typescript-eslint/no-non-null-assertion': 'off',
      '@typescript-eslint/no-unused-vars': ['warn', { argsIgnorePattern: '^_', varsIgnorePattern: '^_' }],
      'no-empty': ['warn', { allowEmptyCatch: true }],
      // react-hooks v7 新增的 React Compiler 建议规则：既有代码存量 34 处触发
      // （set-state-in-effect 26 / refs 6 / preserve-manual-memoization 2）。
      // 属性能与模式建议，不是正确性缺陷 → 降为 warn 保留可见性，作为技术债逐步收口。
      'react-hooks/set-state-in-effect': 'warn',
      'react-hooks/refs': 'warn',
      'react-hooks/preserve-manual-memoization': 'warn',
    },
  },
)
