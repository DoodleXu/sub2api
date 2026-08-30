/** @type {import('tailwindcss').Config} */
export default {
  content: ['./index.html', './src/**/*.{vue,ts}'],
  darkMode: 'class',
  theme: {
    extend: {
      colors: {
        ink: '#dce8f3',
        muted: '#8ea6b9',
        panel: '#102536',
        surface: '#0b1b2a',
        teal: {
          300: '#66e1d1',
          400: '#2dd4bf',
          500: '#14b8a6',
          600: '#0d9488',
        },
      },
      boxShadow: {
        soft: '0 20px 60px rgba(1, 11, 20, .34)',
      },
    },
  },
  plugins: [],
}
