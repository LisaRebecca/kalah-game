package mcts;

import info.kwarc.kalah.KalahState;

import java.util.ArrayList;

public class KalahNode {
    KalahState state;

    KalahNode parent;
    ArrayList<Integer> children; // possible moves
    int countVisited;

    int wins;

    public KalahNode(KalahState ks){
        this.state = ks;

    }

    public KalahNode getRandomChildNode(){
        int move = children.get(0);
        KalahState newState = new KalahState(this.state);
        newState.doMove(move);
        return new KalahNode(newState);
    }

    public void expand(){
        this.children = this.state.getMoves();
    }

    public int simulateRandomPlayout(){
        return 0;
    }

    public void backpropagate(int playoutResult){

    }


}
