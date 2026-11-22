import React from 'react'
import ReactDOM from 'react-dom/client'

import App from './App'
import './design/tokens.css'

const container = document.getElementById('root')
if (container === null) throw new Error('missing #root container')

ReactDOM.createRoot(container).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>
)