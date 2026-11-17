import { mount } from 'svelte';
import App from './App.svelte';
// One stylesheet for the web app and the desktop app.
import '../../../web/src/app.css';

mount(App, { target: document.getElementById('app')! });
