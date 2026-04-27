import { mount } from 'svelte';
import App from './App.svelte';
import { keyboard } from './lib/keyboard/engine.svelte';
import './app.css';

const target = document.getElementById('app');
if (!target) throw new Error('#app element missing from index.html');

// One global keydown listener for the whole admin shell.
keyboard.init();

const app = mount(App, { target });

export default app;
