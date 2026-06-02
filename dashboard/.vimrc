" Project-local vimrc — loaded because the global vimrc has `set exrc`.
" Launch vim from this directory (the repo root) for these mappings to apply.

nnoremap <silent> <leader>t :!clear && make test<CR>
nnoremap <silent> <leader>b :!clear && make build<CR>
nnoremap <silent> <leader>r :!clear && make run<CR>
nnoremap <silent> <leader>c :!clear && make clean<CR>
